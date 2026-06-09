package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type CheckResponse struct {
	Allowed    bool   `json:"allowed"`
	Remaining  int    `json:"remaining"`
	RetryAfter int    `json:"retry_after"`
	Key        string `json:"key"`
	Algorithm  string `json:"algorithm"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

var errBadParam = errors.New("bad param")

// CheckHandler returns the HTTP handler for POST /check.
//
// Configuration precedence: a per-key config stored in Redis (see ConfigHandler)
// fully overrides query-param defaults. When no stored config exists, params
// from the URL are used, falling back to the same defaults as Day 2.
//
// emitter may be nil, in which case no analytics events are produced.
func CheckHandler(rdb *redis.Client, emitter *EventEmitter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed; use POST")
			return
		}

		q := r.URL.Query()
		key := q.Get("key")
		if key == "" {
			writeError(w, http.StatusBadRequest, "query parameter 'key' is required")
			return
		}

		cfg, err := resolveConfig(r.Context(), rdb, key, q)
		if err != nil {
			if errors.Is(err, errBadParam) {
				// resolveConfig returns wrapped errors; surface the message.
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			slog.ErrorContext(r.Context(), "load config",
				"key", key, "err", err.Error())
			writeError(w, http.StatusInternalServerError, "rate limit check failed")
			return
		}

		var (
			allowed    bool
			remaining  int
			retryAfter int
			reset      int64
			limitHdr   int
		)

		switch cfg.Algorithm {
		case AlgoFixed:
			allowed, remaining, retryAfter, reset, err = CheckFixedWindow(
				r.Context(), rdb, key, cfg.Limit, cfg.Window)
			limitHdr = cfg.Limit
		case AlgoSliding:
			allowed, remaining, retryAfter, reset, err = CheckSlidingWindow(
				r.Context(), rdb, key, cfg.Limit, cfg.Window)
			limitHdr = cfg.Limit
		case AlgoToken:
			allowed, remaining, retryAfter, reset, err = CheckTokenBucket(
				r.Context(), rdb, key, cfg.Capacity, cfg.Refill)
			limitHdr = cfg.Capacity
		default:
			writeError(w, http.StatusBadRequest, "algorithm must be one of: fixed, sliding, token")
			return
		}

		if err != nil {
			slog.ErrorContext(r.Context(), "rate limit check failed",
				"key", key, "algorithm", cfg.Algorithm, "err", err.Error())
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				writeError(w, http.StatusServiceUnavailable, "request cancelled")
				return
			}
			writeError(w, http.StatusInternalServerError, "rate limit check failed")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limitHdr))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
		if reset > 0 {
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset, 10))
		}

		status := http.StatusOK
		if !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			status = http.StatusTooManyRequests
		}

		latency := time.Since(start)
		slog.InfoContext(r.Context(), "check",
			"key", key,
			"algorithm", cfg.Algorithm,
			"allowed", allowed,
			"remaining", remaining,
			"status", status,
			"latency_ms", latency.Milliseconds(),
		)

		if emitter != nil {
			emitter.Emit(Event{
				Key:       key,
				Algorithm: cfg.Algorithm,
				Allowed:   allowed,
				Status:    status,
				TS:        start,
			})
		}

		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(CheckResponse{
			Allowed:    allowed,
			Remaining:  remaining,
			RetryAfter: retryAfter,
			Key:        key,
			Algorithm:  cfg.Algorithm,
		})
	}
}

// resolveConfig returns the effective config for a /check request: a stored
// per-key config if one exists, otherwise the config derived from query params
// (with the same defaults as Day 2).
func resolveConfig(ctx context.Context, rdb *redis.Client, key string, q map[string][]string) (*LimitConfig, error) {
	stored, err := GetConfig(ctx, rdb, key)
	if err == nil {
		return stored, nil
	}
	if !errors.Is(err, ErrConfigNotFound) {
		return nil, err
	}

	algo := first(q, "algorithm")
	if algo == "" {
		algo = AlgoFixed
	}

	cfg := &LimitConfig{Algorithm: algo}
	switch algo {
	case AlgoFixed, AlgoSliding:
		limit, perr := parsePositiveInt(first(q, "limit"), 10)
		if perr != nil {
			return nil, wrapBadParam("limit must be a positive integer")
		}
		window, perr := parsePositiveInt(first(q, "window"), 60)
		if perr != nil {
			return nil, wrapBadParam("window must be a positive integer")
		}
		cfg.Limit = limit
		cfg.Window = window
	case AlgoToken:
		capacity, perr := parsePositiveInt(first(q, "capacity"), 10)
		if perr != nil {
			return nil, wrapBadParam("capacity must be a positive integer")
		}
		refill, perr := parsePositiveFloat(first(q, "refill"), 1.0)
		if perr != nil {
			return nil, wrapBadParam("refill must be a positive number")
		}
		cfg.Capacity = capacity
		cfg.Refill = refill
	default:
		return nil, wrapBadParam("algorithm must be one of: fixed, sliding, token")
	}
	return cfg, nil
}

func first(q map[string][]string, name string) string {
	if vs, ok := q[name]; ok && len(vs) > 0 {
		return vs[0]
	}
	return ""
}

// badParamError pairs a public message with the sentinel errBadParam so the
// handler can route on it.
type badParamError struct{ msg string }

func (e *badParamError) Error() string { return e.msg }
func (e *badParamError) Unwrap() error { return errBadParam }

func wrapBadParam(msg string) error { return &badParamError{msg: msg} }

// ConfigHandler returns the HTTP handler for /config (GET and PUT).
//
//	GET  /config?key=X        -> 200 with the stored config, or 404 if absent.
//	PUT  /config?key=X        -> store config (from query params or JSON body),
//	                             returns the stored config as JSON.
func ConfigHandler(rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		if key == "" {
			writeError(w, http.StatusBadRequest, "query parameter 'key' is required")
			return
		}

		switch r.Method {
		case http.MethodGet:
			cfg, err := GetConfig(r.Context(), rdb, key)
			if errors.Is(err, ErrConfigNotFound) {
				writeError(w, http.StatusNotFound, "no config stored for key")
				return
			}
			if err != nil {
				slog.ErrorContext(r.Context(), "get config",
					"key", key, "err", err.Error())
				writeError(w, http.StatusInternalServerError, "failed to read config")
				return
			}
			writeJSON(w, http.StatusOK, cfg)

		case http.MethodPut:
			if os.Getenv("ENV") != "dev" {
				writeError(w, http.StatusMethodNotAllowed, "config writes are disabled in production")
				return
			}
			cfg, err := parseConfigInput(r)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			if err := ValidateConfig(cfg); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			if err := SetConfig(r.Context(), rdb, key, cfg); err != nil {
				slog.ErrorContext(r.Context(), "store config",
					"key", key, "err", err.Error())
				writeError(w, http.StatusInternalServerError, "failed to store config")
				return
			}
			slog.InfoContext(r.Context(), "config updated",
				"key", key,
				"algorithm", cfg.Algorithm,
				"limit", cfg.Limit,
				"window", cfg.Window,
				"capacity", cfg.Capacity,
				"refill", cfg.Refill,
			)
			writeJSON(w, http.StatusOK, cfg)

		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed; use GET or PUT")
		}
	}
}

// parseConfigInput reads a LimitConfig from a JSON body (when Content-Type is
// application/json) or from the request's query parameters.
func parseConfigInput(r *http.Request) (*LimitConfig, error) {
	cfg := &LimitConfig{}
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(cfg); err != nil {
			return nil, errors.New("invalid JSON body: " + err.Error())
		}
		return cfg, nil
	}

	q := r.URL.Query()
	cfg.Algorithm = q.Get("algorithm")
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, errors.New("limit must be an integer")
		}
		cfg.Limit = n
	}
	if v := q.Get("window"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, errors.New("window must be an integer")
		}
		cfg.Window = n
	}
	if v := q.Get("capacity"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, errors.New("capacity must be an integer")
		}
		cfg.Capacity = n
	}
	if v := q.Get("refill"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, errors.New("refill must be a number")
		}
		cfg.Refill = f
	}
	return cfg, nil
}

func parsePositiveInt(raw string, def int) (int, error) {
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return 0, errBadParam
	}
	return v, nil
}

func parsePositiveFloat(raw string, def float64) (float64, error) {
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v <= 0 {
		return 0, errBadParam
	}
	return v, nil
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

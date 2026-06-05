package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
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

// CheckHandler returns the HTTP handler for POST /check. The algorithm query
// param (default "fixed") selects between fixed-window, sliding-window, and
// token-bucket implementations.
func CheckHandler(rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

		algo := q.Get("algorithm")
		if algo == "" {
			algo = AlgoFixed
		}

		var (
			allowed    bool
			remaining  int
			retryAfter int
			limitHdr   int
			err        error
		)

		switch algo {
		case AlgoFixed, AlgoSliding:
			limit, perr := parsePositiveInt(q.Get("limit"), 10)
			if perr != nil {
				writeError(w, http.StatusBadRequest, "limit must be a positive integer")
				return
			}
			window, perr := parsePositiveInt(q.Get("window"), 60)
			if perr != nil {
				writeError(w, http.StatusBadRequest, "window must be a positive integer")
				return
			}
			limitHdr = limit
			if algo == AlgoFixed {
				allowed, remaining, retryAfter, err = CheckFixedWindow(r.Context(), rdb, key, limit, window)
			} else {
				allowed, remaining, retryAfter, err = CheckSlidingWindow(r.Context(), rdb, key, limit, window)
			}

		case AlgoToken:
			capacity, perr := parsePositiveInt(q.Get("capacity"), 10)
			if perr != nil {
				writeError(w, http.StatusBadRequest, "capacity must be a positive integer")
				return
			}
			refill, perr := parsePositiveFloat(q.Get("refill"), 1.0)
			if perr != nil {
				writeError(w, http.StatusBadRequest, "refill must be a positive number")
				return
			}
			limitHdr = capacity
			allowed, remaining, retryAfter, err = CheckTokenBucket(r.Context(), rdb, key, capacity, refill)

		default:
			writeError(w, http.StatusBadRequest, "algorithm must be one of: fixed, sliding, token")
			return
		}

		if err != nil {
			log.Printf("[%s] error key=%s algo=%s err=%v",
				time.Now().Format(time.RFC3339), key, algo, err)
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

		status := http.StatusOK
		if !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			status = http.StatusTooManyRequests
		}

		log.Printf("[%s] key=%s algo=%s allowed=%t remaining=%d",
			time.Now().Format(time.RFC3339), key, algo, allowed, remaining)

		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(CheckResponse{
			Allowed:    allowed,
			Remaining:  remaining,
			RetryAfter: retryAfter,
			Key:        key,
			Algorithm:  algo,
		})
	}
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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Error: msg})
}

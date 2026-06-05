package main

import (
	"encoding/json"
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

// CheckHandler returns the HTTP handler for POST /check.
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

		limit, err := parsePositiveInt(q.Get("limit"), 10)
		if err != nil {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}

		window, err := parsePositiveInt(q.Get("window"), 60)
		if err != nil {
			writeError(w, http.StatusBadRequest, "window must be a positive integer")
			return
		}

		allowed, remaining, retryAfter, err := CheckFixedWindow(r.Context(), rdb, key, limit, window)
		if err != nil {
			log.Printf("[%s] error key=%s err=%v", time.Now().Format(time.RFC3339), key, err)
			writeError(w, http.StatusInternalServerError, "rate limit check failed")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))

		status := http.StatusOK
		if !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			status = http.StatusTooManyRequests
		}

		log.Printf("[%s] key=%s algo=fixed allowed=%t remaining=%d",
			time.Now().Format(time.RFC3339), key, allowed, remaining)

		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(CheckResponse{
			Allowed:    allowed,
			Remaining:  remaining,
			RetryAfter: retryAfter,
			Key:        key,
			Algorithm:  "fixed",
		})
	}
}

func parsePositiveInt(raw string, def int) (int, error) {
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return 0, strconv.ErrSyntax
	}
	return v, nil
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Error: msg})
}

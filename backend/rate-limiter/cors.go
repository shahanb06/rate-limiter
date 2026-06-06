package main

import (
	"net/http"
	"os"
)

// NewCORS returns a middleware that adds CORS headers to GET responses and
// short-circuits OPTIONS preflight with 204. Only applied to /analytics/*
// routes — /check is a server-to-server API and does not need CORS.
//
// Origin comes from CORS_ALLOWED_ORIGIN, defaulting to "*" for dev.
func NewCORS() func(http.Handler) http.Handler {
	origin := os.Getenv("CORS_ALLOWED_ORIGIN")
	if origin == "" {
		origin = "*"
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			if origin != "*" {
				w.Header().Set("Vary", "Origin")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

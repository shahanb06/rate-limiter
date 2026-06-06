package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AnalyticsStore is the read-only Postgres interface used by the analytics
// handlers. Keeping SQL behind this interface is what lets /check stay
// Redis-only: the store is never reachable from CheckHandler.
type AnalyticsStore interface {
	ListKeys(ctx context.Context) ([]string, error)
	Summary(ctx context.Context, key string) (SummaryRow, error)
	Timeseries(ctx context.Context, key string, since time.Time) ([]TimeseriesPoint, error)
}

type SummaryRow struct {
	Allowed  int64
	Rejected int64
	Total    int64
}

type TimeseriesPoint struct {
	BucketStart time.Time `json:"bucket_start"`
	Allowed     int64     `json:"allowed"`
	Rejected    int64     `json:"rejected"`
	Total       int64     `json:"total"`
}

// pgStore is the pgxpool-backed AnalyticsStore. Queries are parameterized; no
// caller-supplied string is concatenated into SQL.
type pgStore struct {
	pool *pgxpool.Pool
}

func NewPGStore(pool *pgxpool.Pool) AnalyticsStore {
	return &pgStore{pool: pool}
}

func (s *pgStore) ListKeys(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT key FROM aggregated_metrics ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	keys := make([]string, 0)
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (s *pgStore) Summary(ctx context.Context, key string) (SummaryRow, error) {
	var r SummaryRow
	err := s.pool.QueryRow(ctx, `
		SELECT
		  COALESCE(SUM(allowed_count),  0)::bigint,
		  COALESCE(SUM(rejected_count), 0)::bigint,
		  COALESCE(SUM(total),          0)::bigint
		FROM aggregated_metrics
		WHERE key = $1
	`, key).Scan(&r.Allowed, &r.Rejected, &r.Total)
	return r, err
}

func (s *pgStore) Timeseries(ctx context.Context, key string, since time.Time) ([]TimeseriesPoint, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT bucket_start, allowed_count, rejected_count, total
		FROM aggregated_metrics
		WHERE key = $1 AND bucket_start >= $2
		ORDER BY bucket_start
	`, key, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	points := make([]TimeseriesPoint, 0)
	for rows.Next() {
		var p TimeseriesPoint
		if err := rows.Scan(&p.BucketStart, &p.Allowed, &p.Rejected, &p.Total); err != nil {
			return nil, err
		}
		points = append(points, p)
	}
	return points, rows.Err()
}

// --- handlers ---

// AnalyticsKeysHandler returns the distinct keys present in aggregated_metrics.
// Returns 503 when store is nil (DATABASE_URL unset) or when the query fails.
func AnalyticsKeysHandler(store AnalyticsStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed; use GET")
			return
		}
		if store == nil {
			writeError(w, http.StatusServiceUnavailable, "analytics unavailable")
			return
		}
		keys, err := store.ListKeys(r.Context())
		if err != nil {
			slog.ErrorContext(r.Context(), "analytics list keys", "err", err.Error())
			writeError(w, http.StatusServiceUnavailable, "analytics unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
	}
}

// AnalyticsSummaryHandler returns totals for one key.
func AnalyticsSummaryHandler(store AnalyticsStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed; use GET")
			return
		}
		key := r.URL.Query().Get("key")
		if key == "" {
			writeError(w, http.StatusBadRequest, "query parameter 'key' is required")
			return
		}
		if store == nil {
			writeError(w, http.StatusServiceUnavailable, "analytics unavailable")
			return
		}
		row, err := store.Summary(r.Context(), key)
		if err != nil {
			slog.ErrorContext(r.Context(), "analytics summary", "key", key, "err", err.Error())
			writeError(w, http.StatusServiceUnavailable, "analytics unavailable")
			return
		}
		var rate float64
		if row.Total > 0 {
			rate = float64(row.Rejected) / float64(row.Total)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"key":            key,
			"allowed":        row.Allowed,
			"rejected":       row.Rejected,
			"total":          row.Total,
			"rejection_rate": rate,
		})
	}
}

// AnalyticsTimeseriesHandler returns per-minute buckets for one key since a
// given point. `since` accepts a Go duration (e.g. "1h", "30m") relative to
// now, or an RFC3339 timestamp. Default = last 1h.
func AnalyticsTimeseriesHandler(store AnalyticsStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed; use GET")
			return
		}
		key := r.URL.Query().Get("key")
		if key == "" {
			writeError(w, http.StatusBadRequest, "query parameter 'key' is required")
			return
		}

		since, err := parseSince(r.URL.Query().Get("since"), time.Now())
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		if store == nil {
			writeError(w, http.StatusServiceUnavailable, "analytics unavailable")
			return
		}
		points, err := store.Timeseries(r.Context(), key, since)
		if err != nil {
			slog.ErrorContext(r.Context(), "analytics timeseries",
				"key", key, "since", since, "err", err.Error())
			writeError(w, http.StatusServiceUnavailable, "analytics unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"key":    key,
			"since":  since.UTC().Format(time.RFC3339),
			"points": points,
		})
	}
}

// parseSince accepts a Go duration ("1h", "30m") relative to now, an RFC3339
// timestamp, or empty (defaults to now-1h).
func parseSince(raw string, now time.Time) (time.Time, error) {
	if raw == "" {
		return now.Add(-1 * time.Hour), nil
	}
	if d, err := time.ParseDuration(raw); err == nil {
		if d < 0 {
			d = -d
		}
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	return time.Time{}, errors.New("since must be a Go duration (e.g. '1h') or RFC3339 timestamp")
}

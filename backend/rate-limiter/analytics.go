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
	SummaryByAlgorithm(ctx context.Context, key string) ([]SummaryByAlgoRow, error)
	TimeseriesByAlgorithm(ctx context.Context, key string, since time.Time) ([]TimeseriesAlgoPoint, error)
	Leaderboard(ctx context.Context) ([]LeaderboardRow, error)
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

type SummaryByAlgoRow struct {
	Algorithm string `json:"algorithm"`
	Allowed   int64  `json:"allowed"`
	Rejected  int64  `json:"rejected"`
	Total     int64  `json:"total"`
}

type TimeseriesAlgoPoint struct {
	BucketStart time.Time `json:"bucket_start"`
	Algorithm   string    `json:"algorithm"`
	Allowed     int64     `json:"allowed"`
	Rejected    int64     `json:"rejected"`
	Total       int64     `json:"total"`
}

type LeaderboardRow struct {
	Key      string `json:"key"`
	Allowed  int64  `json:"allowed"`
	Rejected int64  `json:"rejected"`
	Total    int64  `json:"total"`
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

func (s *pgStore) SummaryByAlgorithm(ctx context.Context, key string) ([]SummaryByAlgoRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT algorithm,
		  COALESCE(SUM(allowed_count),  0)::bigint,
		  COALESCE(SUM(rejected_count), 0)::bigint,
		  COALESCE(SUM(total),          0)::bigint
		FROM aggregated_metrics
		WHERE key = $1
		GROUP BY algorithm
		ORDER BY algorithm
	`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]SummaryByAlgoRow, 0)
	for rows.Next() {
		var r SummaryByAlgoRow
		if err := rows.Scan(&r.Algorithm, &r.Allowed, &r.Rejected, &r.Total); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *pgStore) TimeseriesByAlgorithm(ctx context.Context, key string, since time.Time) ([]TimeseriesAlgoPoint, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT bucket_start, algorithm, allowed_count, rejected_count, total
		FROM aggregated_metrics
		WHERE key = $1 AND bucket_start >= $2
		ORDER BY bucket_start, algorithm
	`, key, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]TimeseriesAlgoPoint, 0)
	for rows.Next() {
		var p TimeseriesAlgoPoint
		if err := rows.Scan(&p.BucketStart, &p.Algorithm, &p.Allowed, &p.Rejected, &p.Total); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *pgStore) Leaderboard(ctx context.Context) ([]LeaderboardRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT key,
		  COALESCE(SUM(allowed_count),  0)::bigint,
		  COALESCE(SUM(rejected_count), 0)::bigint,
		  COALESCE(SUM(total),          0)::bigint
		FROM aggregated_metrics
		GROUP BY key
		ORDER BY SUM(total) DESC, key
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]LeaderboardRow, 0)
	for rows.Next() {
		var r LeaderboardRow
		if err := rows.Scan(&r.Key, &r.Allowed, &r.Rejected, &r.Total); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
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
//
// Default (no group_by) returns aggregate counts across all algorithms — the
// Day 7/8 shape. With group_by=algorithm, returns a by_algorithm array with
// per-algorithm allowed/rejected/total/rejection_rate. Any other group_by
// value is a 400 so typos don't silently fall through to the default.
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
		groupBy := r.URL.Query().Get("group_by")
		if groupBy != "" && groupBy != "algorithm" {
			writeError(w, http.StatusBadRequest, "group_by must be 'algorithm' or omitted")
			return
		}
		if store == nil {
			writeError(w, http.StatusServiceUnavailable, "analytics unavailable")
			return
		}

		if groupBy == "algorithm" {
			rows, err := store.SummaryByAlgorithm(r.Context(), key)
			if err != nil {
				slog.ErrorContext(r.Context(), "analytics summary by algorithm", "key", key, "err", err.Error())
				writeError(w, http.StatusServiceUnavailable, "analytics unavailable")
				return
			}
			byAlgo := make([]map[string]any, 0, len(rows))
			for _, row := range rows {
				var rate float64
				if row.Total > 0 {
					rate = float64(row.Rejected) / float64(row.Total)
				}
				byAlgo = append(byAlgo, map[string]any{
					"algorithm":      row.Algorithm,
					"allowed":        row.Allowed,
					"rejected":       row.Rejected,
					"total":          row.Total,
					"rejection_rate": rate,
				})
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"key":          key,
				"by_algorithm": byAlgo,
			})
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
//
// Default (no group_by) collapses across algorithms — the Day 7/8 shape.
// group_by=algorithm returns rows tagged with `algorithm` so the frontend
// can pivot into one line per algorithm.
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
		groupBy := r.URL.Query().Get("group_by")
		if groupBy != "" && groupBy != "algorithm" {
			writeError(w, http.StatusBadRequest, "group_by must be 'algorithm' or omitted")
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

		if groupBy == "algorithm" {
			points, err := store.TimeseriesByAlgorithm(r.Context(), key, since)
			if err != nil {
				slog.ErrorContext(r.Context(), "analytics timeseries by algorithm",
					"key", key, "since", since, "err", err.Error())
				writeError(w, http.StatusServiceUnavailable, "analytics unavailable")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"key":    key,
				"since":  since.UTC().Format(time.RFC3339),
				"points": points,
			})
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

// AnalyticsLeaderboardHandler returns all keys with their lifetime totals
// (allowed/rejected/total/rejection_rate) ordered by total volume desc.
// This is the "zoom out" view that lets the dashboard show every key at once
// rather than needing a key picked first.
func AnalyticsLeaderboardHandler(store AnalyticsStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed; use GET")
			return
		}
		if store == nil {
			writeError(w, http.StatusServiceUnavailable, "analytics unavailable")
			return
		}
		rows, err := store.Leaderboard(r.Context())
		if err != nil {
			slog.ErrorContext(r.Context(), "analytics leaderboard", "err", err.Error())
			writeError(w, http.StatusServiceUnavailable, "analytics unavailable")
			return
		}
		out := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			var rate float64
			if row.Total > 0 {
				rate = float64(row.Rejected) / float64(row.Total)
			}
			out = append(out, map[string]any{
				"key":            row.Key,
				"allowed":        row.Allowed,
				"rejected":       row.Rejected,
				"total":          row.Total,
				"rejection_rate": rate,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"rows": out})
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

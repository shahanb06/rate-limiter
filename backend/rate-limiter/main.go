package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	rdb, err := NewRedisClient()
	if err != nil {
		slog.Error("redis connect failed", "err", err.Error())
		os.Exit(1)
	}
	defer rdb.Close()

	emitter := NewEventEmitter(rdb, "rl:events", 1024, 10000)
	emitterCtx, emitterCancel := context.WithCancel(context.Background())
	go emitter.Run(emitterCtx)

	// Postgres is optional. Empty or invalid DATABASE_URL means analytics
	// endpoints will 503 but /check, /config, /health stay fully functional.
	var (
		pgPool *pgxpool.Pool
		store  AnalyticsStore
	)
	if dbURL := os.Getenv("DATABASE_URL"); dbURL != "" {
		// pgxpool.New is lazy — no connection attempt until first query.
		pool, err := pgxpool.New(context.Background(), dbURL)
		if err != nil {
			slog.Warn("invalid DATABASE_URL; analytics disabled", "err", err.Error())
		} else {
			pgPool = pool
			store = NewPGStore(pool)
			slog.Info("analytics enabled")
		}
	} else {
		slog.Info("DATABASE_URL unset; analytics disabled")
	}
	if pgPool != nil {
		defer pgPool.Close()
	}

	cors := NewCORS()
	mux := http.NewServeMux()
	mux.HandleFunc("/check", CheckHandler(rdb, emitter))
	mux.HandleFunc("/config", ConfigHandler(rdb))
	mux.HandleFunc("/health", HealthHandler(rdb, emitter, pgPool))
	mux.Handle("/analytics/keys", cors(AnalyticsKeysHandler(store)))
	mux.Handle("/analytics/summary", cors(AnalyticsSummaryHandler(store)))
	mux.Handle("/analytics/timeseries", cors(AnalyticsTimeseriesHandler(store)))

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("server starting", "port", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-stop:
		slog.Info("shutdown signal", "signal", sig.String())
	case err := <-serverErr:
		slog.Error("server error", "err", err.Error())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("shutdown error", "err", err.Error())
	}

	emitterCancel()
	select {
	case <-emitter.Done():
	case <-time.After(3 * time.Second):
		slog.Warn("event emitter drain timeout")
	}

	slog.Info("server stopped")
}

// HealthHandler reports liveness and observability info. Status code is driven
// only by Redis reachability — Postgres is optional, so an unreachable Postgres
// does not flip the response to 503. pool may be nil (analytics disabled).
func HealthHandler(rdb *redis.Client, emitter *EventEmitter, pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		redisErr := rdb.Ping(ctx).Err()

		status := http.StatusOK
		redisState := "connected"
		if redisErr != nil {
			status = http.StatusServiceUnavailable
			redisState = "disconnected"
		}

		pgState := "disabled"
		if pool != nil {
			if err := pool.Ping(ctx); err != nil {
				pgState = "unavailable"
			} else {
				pgState = "connected"
			}
		}

		var dropped uint64
		if emitter != nil {
			dropped = emitter.Dropped()
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":         "ok",
			"redis":          redisState,
			"postgres":       pgState,
			"events_dropped": dropped,
		})
	}
}

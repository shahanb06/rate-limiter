# Rate Limiter + Analytics System

A full-stack rate-limiting service with an end-to-end analytics pipeline.
A distributed Go + Redis rate limiter exposes `/check` for three algorithms
(fixed window, sliding window, token bucket). Each decision is published to a
Redis Stream and consumed by a Python worker that lands raw events into
PostgreSQL and rolls them up into per-minute aggregates. A Go analytics API
reads those aggregates, and a Next.js dashboard polls the API to render live
totals and timeseries.

The rate limiter is deployed on Fly.io. The dashboard runs locally against
the API; Vercel deploy is upcoming.

## Live demo

Deployed on Fly.io (region `iad`) with Upstash Redis (`us-east-1`):

**https://rate-limiter-shahanb06.fly.dev**

```bash
# Service health (Redis reachability check)
curl https://rate-limiter-shahanb06.fly.dev/health

# Trip the rate limiter: first 5 return 200, then 429s for the rest of the window
for i in $(seq 1 7); do
  curl -i -X POST "https://rate-limiter-shahanb06.fly.dev/check?key=demo&algorithm=fixed&limit=5&window=60" \
    | head -8
  echo
done
```

The Fly machines are configured for scale-to-zero — the first request after
idle may add ~300 ms of cold-start latency. The Fly deployment runs the
limiter only; the analytics pipeline and dashboard run locally via
docker-compose (see [Quick start](#quick-start)).

## Status

Day 8 of 12 — rate limiter live on Fly, full local analytics stack
(PostgreSQL + Python worker + Next.js dashboard), benchmarked at 844 req/s
sustained, ~5,000 req/s burst (p95 ~2 ms) on a laptop (see
[Benchmarks](#benchmarks)). Built so far: three algorithms with atomic Redis
Lua scripts, per-key configuration in Redis (`PUT`/`GET /config`),
fire-and-forget event emission, Python worker consuming a Redis Stream via
consumer groups (at-least-once delivery), idempotent per-minute aggregation
into Postgres, analytics read API (`/analytics/keys`, `/summary`,
`/timeseries`), CORS-scoped to the analytics surface only, and a Next.js
dashboard polling every 7 s with key picker, summary tiles, and an
allowed-vs-rejected area chart.

## Architecture

```
        client
          │  POST /check
          ▼
    ┌──────────────┐                     ┌───────────────────────────┐
    │   Go API     │  fire-and-forget    │   Redis Stream            │
    │  /check      │ ──────────────────▶ │   (rl:events)             │
    │              │                     └───────────────────────────┘
    │  atomic Lua  │                                   │
    │  fixed /     │ ◀──┐                              │  XREADGROUP
    │  sliding /   │    │                              │  rl-workers
    │  token       │    │ rate-limit state             ▼
    └──────────────┘    │                ┌───────────────────────────┐
          │             │                │   Python worker           │
          │ reads       │                │   batched INSERT + XACK   │
          ▼             │                └───────────────────────────┘
    ┌──────────────┐    │                              │
    │   Redis      │ ───┘                              │  rolls up every 15 s
    └──────────────┘                                   ▼
                                         ┌───────────────────────────┐
                                         │   PostgreSQL              │
                                         │  ┌─────────────────────┐  │
                                         │  │ raw_events          │  │
                                         │  └─────────────────────┘  │
                                         │  ┌─────────────────────┐  │
                                         │  │ aggregated_metrics  │  │ ◀── per-minute
                                         │  └─────────────────────┘  │     UPSERT
                                         └───────────────────────────┘
                                                       ▲
                                                       │ pgx/v5 pool
    ┌──────────────────────────────────────────────────┴────────────┐
    │                  Go API — /analytics/*                        │
    │   /keys      /summary?key=X      /timeseries?key=X&since=1h   │
    └────────────────────────────┬──────────────────────────────────┘
                                 │  fetch (CORS)
                                 ▼
                      ┌───────────────────────┐
                      │  Next.js dashboard    │
                      │  localhost:3000       │
                      │  7 s poll · recharts  │
                      └───────────────────────┘
```

Design properties worth calling out:

- **At-least-once + idempotent aggregation.** The worker XACKs only after the
  INSERT batch commits, so a crash between commit and ack can redeliver and
  duplicate rows in `raw_events`. The per-minute rollup groups by
  `(key, algorithm, bucket_start)` and UPSERTs *absolute* counts via
  `ON CONFLICT … DO UPDATE`, so duplicate raw rows are absorbed and the
  aggregates stay correct.
- **Fire-and-forget event emission.** `/check` does not wait on the Redis
  Stream publish; the emitter has a bounded buffer with a dropped-event
  counter exposed on `/health`. Analytics throughput backpressure cannot
  raise `/check` latency.
- **Postgres-optional.** `DATABASE_URL` is read at startup; if unset or
  unreachable, the `/analytics/*` endpoints return 503 but `/check`,
  `/config`, and `/health` keep working. The rate limiter survives an
  analytics DB outage.
- **CORS scoped to reads only.** The dashboard runs on a different origin
  than the API, so CORS headers are required — but they're applied only to
  `/analytics/*`. `/check` is a server-to-server API and stays
  non-browser-callable. Allowed origin is configurable via
  `CORS_ALLOWED_ORIGIN` (defaults to `*` for dev).

## Benchmarks

Load tested with k6 against the local docker-compose stack on an 8-core arm64
laptop. Full results (per-scenario detail, per-algorithm latency, methodology
and caveats) are in [`benchmarks/results.md`](benchmarks/results.md).

| Scenario | Throughput | p50 | p95 | p99 | HTTP failures |
|---|---|---|---|---|---|
| sustained (1000 req/s × 30s)    | 844 req/s | 0.76 ms | 1.47 ms | 21.7 ms | 0 / 37,999 |
| burst (5000 req/s × 10s)        | **4,995 req/s** | 0.41 ms | 2.10 ms | 37.3 ms | 0 / 49,958 |
| mixed (600 req/s across 3 algos)| 600 req/s | 0.82 ms | 1.37 ms | 2.43 ms | 0 / 18,000 |

Atomic Lua paths (sliding/token) come in within ~10% of the fixed-window
`INCR` path:

| Algorithm | p95 | p99 |
|---|---|---|
| fixed   | 1.30 ms | 2.41 ms |
| sliding | 1.37 ms | 2.52 ms |
| token   | 1.40 ms | 2.35 ms |

Reproduce with:

```bash
docker compose up -d --build
SCENARIO=sustained docker run --rm -i --add-host=host.docker.internal:host-gateway \
  -v "$(pwd):/scripts" -e BASE_URL=http://host.docker.internal:8080 -e SCENARIO=$SCENARIO \
  grafana/k6 run /scripts/k6-load-test.js
```

## Quick start

Bring up Redis, Postgres, the Go API, and the Python worker:

```bash
docker compose up -d --build
```

Exercise each algorithm. Each example sends 10 requests to fill the budget;
the 11th will come back as `429`.

```bash
# Fixed window — 10 requests per 60s, window aligned to wall clock
curl -i -X POST "http://localhost:8080/check?key=test&algorithm=fixed&limit=10&window=60"

# Sliding window — same shape, but the window slides with each request
curl -i -X POST "http://localhost:8080/check?key=test&algorithm=sliding&limit=10&window=60"

# Token bucket — capacity 10, refills at 1 token/sec
curl -i -X POST "http://localhost:8080/check?key=test&algorithm=token&capacity=10&refill=1"
```

Loop one of them to see the limiter trip:

```bash
for i in $(seq 1 12); do
  curl -s -o /dev/null -w "%{http_code}\n" \
    -X POST "http://localhost:8080/check?key=test&algorithm=sliding&limit=10&window=60"
done
```

Wait ~15 s for the worker to flush a bucket, then read the analytics:

```bash
curl "http://localhost:8080/analytics/keys"
curl "http://localhost:8080/analytics/summary?key=test"
curl "http://localhost:8080/analytics/timeseries?key=test&since=1h"
```

Check service health:

```bash
curl http://localhost:8080/health
```

Tear everything down:

```bash
docker compose down
```

### Run the dashboard

```bash
cd frontend
npm install
npm run dev
```

Then open http://localhost:3000. The key picker lists keys with aggregated
data, and the summary + chart refresh every 7 s. API base URL is configurable
via `NEXT_PUBLIC_API_BASE_URL` (defaults to `http://localhost:8080`).

## API

### `POST /check`

| Query param | Required | Default | Description                            |
|-------------|----------|---------|----------------------------------------|
| `key`       | yes      | —       | Identifier being rate-limited          |
| `algorithm` | no       | `fixed` | One of `fixed`, `sliding`, `token`     |
| `limit`     | no       | 10      | Max requests in the window (fixed/sliding) |
| `window`    | no       | 60      | Window length in seconds (fixed/sliding)   |
| `capacity`  | no       | 10      | Bucket capacity (token)                |
| `refill`    | no       | 1       | Tokens per second (token)              |

Returns JSON `{ allowed, remaining, retry_after, key, algorithm }` and the
headers `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset`,
and (when blocked) `Retry-After`. Status is `200` when allowed, `429` when
blocked. A per-key config stored via `PUT /config` overrides query-param
defaults.

### `GET` / `PUT /config?key=X`

`GET` returns the stored config for a key (404 if absent). `PUT` writes one,
accepting either query params or a JSON body (`{algorithm, limit, window,
capacity, refill}`). Stored configs take precedence over `/check` query
params.

### `GET /analytics/keys`

Returns `{ "keys": [...] }` — distinct keys present in `aggregated_metrics`.

### `GET /analytics/summary?key=X`

Returns `{ key, allowed, rejected, total, rejection_rate }` aggregated over
all buckets for that key.

### `GET /analytics/timeseries?key=X&since=1h`

Returns per-minute buckets since the given offset. `since` accepts a Go
duration (`1h`, `30m`, …) or an RFC3339 timestamp; default `1h`.

### `GET /health`

Returns `{ status, redis, postgres, events_dropped }`. Status code is driven
by Redis only — Postgres is optional, so an unreachable analytics DB does
not flip the response to 503.

## Testing

The Go suite runs with zero external setup — no Docker, no Redis — using
[miniredis](https://github.com/alicebob/miniredis) as an in-process backend:

```bash
cd backend/rate-limiter && go test ./... -race
```

What's covered:

- Table-driven unit tests for all three algorithms (fixed window, sliding
  window, token bucket), including boundary behavior and refill / aging.
- HTTP handler tests for `/check`, `/config`, `/health`, and the
  `/analytics/*` routes (using a hand-rolled `AnalyticsStore` fake).
- CORS middleware tests: analytics responses carry the CORS headers, OPTIONS
  preflight returns 204, and `/check` does **not** carry CORS headers.
- A concurrency test that fires **100 goroutines at a limit of 50** and
  asserts exactly 50 are admitted — proves the Redis Lua scripts (and the
  fixed-window `INCR`) prevent over-admission under contention.
- The whole suite is race-clean (`go test -race`).

Frontend type-checks with:

```bash
cd frontend && npx tsc --noEmit
```

## Tech stack

| Layer                | Technology                                       |
|----------------------|--------------------------------------------------|
| API service          | Go 1.25, net/http (stdlib), go-redis/v9          |
| Rate-limit store     | Redis 7 (atomic Lua scripts)                     |
| Event transport      | Redis Streams + consumer groups                  |
| Aggregation worker   | Python 3, redis-py, psycopg                      |
| Analytics store      | PostgreSQL 16 (pgx/v5 pool)                      |
| Dashboard            | Next.js 14, React 18, TypeScript, Tailwind, recharts |
| Container            | Docker + Compose                                 |
| Deploy (limiter)     | Fly.io (`iad`) + Upstash Redis (`us-east-1`)     |

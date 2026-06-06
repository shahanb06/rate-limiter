# Rate Limiter + Analytics System

A distributed rate-limiting service written in Go and backed by Redis. Exposes
an HTTP API that lets clients enforce per-key request limits with configurable
algorithms (starting with fixed-window) and will grow over 12 days to include
multiple algorithms (sliding window, token bucket), analytics, and a dashboard.

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
idle may add ~300 ms of cold-start latency.

## Status

Day 4 of 12 — live at https://rate-limiter-shahanb06.fly.dev, benchmarked at
844 req/s sustained, ~5,000 req/s burst (p95 ~2ms) on a laptop (see
[Benchmarks](#benchmarks)). Three
algorithms (fixed window, sliding window, token bucket) with atomic Redis
Lua scripts, per-key configuration stored in Redis (`PUT`/`GET /config`),
production HTTP headers (`X-RateLimit-*`, `Retry-After`), structured JSON
logging via `slog` with request latency, validated JSON error responses.

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

Start Redis and the rate-limiter service:

```bash
docker-compose up --build
```

In another terminal, exercise each algorithm. Each example sends 10 requests
to fill the budget; the 11th will come back as `429`.

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

Check service health:

```bash
curl http://localhost:8080/health
```

Tear everything down:

```bash
docker-compose down
```

## Testing

The service ships with a Go test suite that runs with zero external setup —
no Docker, no Redis — using [miniredis](https://github.com/alicebob/miniredis)
as an in-process backend.

```bash
cd backend/rate-limiter && go test ./... -race
```

What's covered:

- Table-driven unit tests for all three algorithms (fixed window, sliding
  window, token bucket), including boundary behavior and refill / aging.
- HTTP handler tests for `/check` and `/health` — status codes, JSON body,
  and the `X-RateLimit-*` / `Retry-After` headers.
- A concurrency test that fires **100 goroutines at a limit of 50** and
  asserts exactly 50 are admitted — proves the Redis Lua scripts (and the
  fixed-window `INCR`) prevent over-admission under contention.
- The whole suite is verified race-clean (`go test -race`).

## API

### `POST /check`

| Query param | Required | Default | Description                            |
|-------------|----------|---------|----------------------------------------|
| `key`       | yes      | —       | Identifier being rate-limited          |
| `limit`     | no       | 10      | Max requests allowed within the window |
| `window`    | no       | 60      | Window length in seconds               |

Returns JSON `{ allowed, remaining, retry_after, key, algorithm }` and the
headers `X-RateLimit-Limit`, `X-RateLimit-Remaining`, and (when blocked)
`Retry-After`. Status is `200` when allowed, `429` when blocked.

### `GET /health`

Returns `{ "status": "ok", "redis": "connected" }` when Redis is reachable.

## Tech stack

| Layer        | Technology         |
|--------------|--------------------|
| Language     | Go 1.21            |
| Cache / store| Redis 7            |
| HTTP         | net/http (stdlib)  |
| Redis client | go-redis/v9        |
| Container    | Docker + Compose   |

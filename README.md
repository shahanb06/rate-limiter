# Rate Limiter + Analytics System

A distributed rate-limiting service written in Go and backed by Redis. Exposes
an HTTP API that lets clients enforce per-key request limits with configurable
algorithms (starting with fixed-window) and will grow over 12 days to include
multiple algorithms (sliding window, token bucket), analytics, and a dashboard.

## Status

**Day 2 of 12** — three algorithms (fixed window, sliding window, token
bucket), all using Redis (Lua scripts for sliding + token), `/check` +
`/health` endpoints, Docker setup.

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

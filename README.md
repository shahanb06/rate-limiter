# Rate Limiter + Analytics System

A distributed rate-limiting service written in Go and backed by Redis. Exposes
an HTTP API that lets clients enforce per-key request limits with configurable
algorithms (starting with fixed-window) and will grow over 12 days to include
multiple algorithms (sliding window, token bucket, leaky bucket), analytics,
and a dashboard.

## Status

**Day 1 of 12** — fixed-window algorithm, HTTP `/check` + `/health` endpoints,
Redis backend, Docker setup.

## Quick start

Start Redis and the rate-limiter service:

```bash
docker-compose up --build
```

In another terminal, send a request:

```bash
curl -i -X POST "http://localhost:8080/check?key=test&limit=10&window=60"
```

Loop to see the limiter trip after the 10th request:

```bash
for i in $(seq 1 12); do
  curl -s -o /dev/null -w "%{http_code}\n" \
    -X POST "http://localhost:8080/check?key=test&limit=10&window=60"
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

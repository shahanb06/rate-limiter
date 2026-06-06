# Benchmarks

Results from [`k6-load-test.js`](../k6-load-test.js) run against the local
`docker-compose` stack (rate-limiter + Redis 7-alpine, both in containers).
k6 itself runs in the `grafana/k6` Docker image and reaches the service via
`host.docker.internal`.

## Hardware

| | |
|-|-|
| Machine    | Apple Silicon (arm64) laptop |
| Cores      | 8 |
| Memory     | 8 GB |
| Docker     | Docker Desktop 29.5.2, Compose v5.1.4 |
| Redis      | 7-alpine, in-container (loopback to rate-limiter) |
| Backend    | Go 1.21, single container, default Go runtime settings |

These are laptop numbers measured through Docker Desktop's userland network
stack — not a tuned benchmark rig. They establish a floor for what the
service can do on commodity hardware; a real server with kernel-level
networking will do meaningfully better.

## Headline numbers

| Scenario | Target rate | Achieved | p50 | p95 | p99 | HTTP failures |
|---|---|---|---|---|---|---|
| sustained   | 1000 req/s (30s) | **844 req/s** avg, 1000 req/s in steady state | 0.76 ms | 1.47 ms | 21.7 ms | 0 / 37,999 |
| burst       | 5000 req/s (10s) | **4995 req/s** | 0.41 ms | 2.10 ms | 37.3 ms | 0 / 49,958 |
| mixed_algos | 600 req/s split 3 ways (30s) | 600 req/s | 0.82 ms | 1.37 ms | 2.43 ms | 0 / 18,000 |

All three scenarios passed the configured thresholds (p95 < 50 ms,
http_req_failed < 1%). Zero 5xx and zero connection errors across all ~106k
requests; the only non-2xx responses were 429s, which are the rate limiter
doing its job (k6 is configured to treat 429 as an *expected* status code,
not a failure).

## Per-algorithm latency (mixed_algos run)

The mixed scenario exercises all three algorithms at the same rate. Per-algo
latency tagged via `tags: { algo: ... }` on each request:

| Algorithm | p50 | p95 | p99 | max |
|---|---|---|---|---|
| fixed   | 0.79 ms | 1.30 ms | 2.41 ms | 12.0 ms |
| sliding | 0.83 ms | 1.37 ms | 2.52 ms | 12.9 ms |
| token   | 0.82 ms | 1.40 ms | 2.35 ms | 12.1 ms |

The sliding-window and token-bucket algorithms (atomic Lua scripts) come in
within ~10% of the fixed-window `INCR` path — atomicity isn't free, but the
overhead is small in absolute terms.

## Detailed results

### sustained — ramp to 1000 req/s, hold 30s

```
http_reqs ........... 37,999 total, 844.5 req/s avg
http_req_duration ... avg=1.32ms  med=0.76ms  p90=1.05ms  p95=1.47ms  p99=21.7ms  max=127ms
http_req_failed ..... 0.00%  (0 / 37,999)
rejections (429) .... 44.80% (17,023 / 37,999)
dropped iterations .. 0
```

The 1000 req/s target is hit during the steady-state window; the lower
average rate reflects the 10s ramp-up and 5s ramp-down. The high rejection
rate is an artifact of the test's key distribution: with `maxVUs=200`,
1000 req/s for 30s means each VU's key sees ~150 requests, exceeding the
configured `limit=100/window=60s`. The latency numbers include both 200s
and 429s — both paths are sub-millisecond at the median.

### burst — 5000 req/s flat for 10s

```
http_reqs ........... 49,958 total, 4995 req/s
http_req_duration ... avg=1.24ms  med=0.41ms  p90=0.69ms  p95=2.10ms  p99=37.3ms  max=59.4ms
http_req_failed ..... 0.00%  (0 / 49,958)
rejections (429) .... 4.78%  (2,388 / 49,958)
dropped iterations .. 44 (0.09%)
```

The service held 5000 req/s with zero HTTP errors. p95 stayed at ~2 ms, p99
climbed to ~37 ms (likely from cold connections during the initial spike).
k6 dropped 44 iterations (under 0.1%) — i.e. couldn't *issue* them on
schedule under its own VU pressure, not a service-side failure.

This is the practical ceiling we tested on this laptop. The service was not
the bottleneck on this run (no 5xx, no timeouts) — further headroom would
require pushing past Docker Desktop's network stack to confirm.

### mixed_algos — 600 req/s, evenly split across fixed/sliding/token

```
http_reqs ........... 18,000 total, 600 req/s
http_req_duration ... avg=0.88ms  med=0.82ms  p90=1.05ms  p95=1.37ms  p99=2.43ms  max=12.9ms
http_req_failed ..... 0.00%  (0 / 18,000)
rejections (429) .... 20.18% (3,632 / 18,000)
```

All three algorithms cleared the p95 < 50 ms threshold individually (see
table above). The rejection rate reflects the same per-VU-key behavior as
sustained.

## Reproducing

```bash
# 1. Start the stack
docker compose up -d --build

# 2. Run a scenario (k6 in Docker, reaches host via host.docker.internal)
docker run --rm -i \
  --add-host=host.docker.internal:host-gateway \
  -v "$(pwd):/scripts" \
  -e BASE_URL=http://host.docker.internal:8080 \
  -e SCENARIO=sustained \
  grafana/k6 run /scripts/k6-load-test.js \
  --summary-export=/scripts/benchmarks/sustained-summary.json

# 3. Available SCENARIO values: sustained | burst | mixed_algos
```

The JSON summaries (`sustained-summary.json`, `burst-summary.json`,
`mixed-summary.json`) are committed alongside this file for traceability.

## Caveats

- **Test methodology**: keys are derived from `__VU`, so VUs that recycle
  reuse the same Redis bucket. Sustained-load rejection rates reflect this
  rather than real-world clients each having a fresh budget.
- **Network stack**: Docker Desktop on macOS routes container traffic
  through a userland proxy. Bare-metal or Linux-host numbers will be
  meaningfully better, especially at p99.
- **No Redis tuning**: vanilla `redis:7-alpine` with default config. A
  production Redis (cluster, tuned `tcp-backlog`, etc.) would push the
  ceiling higher.

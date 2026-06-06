-- raw_events stores every /check observation streamed from the Go service.
--
-- Delivery semantics: at-least-once. The worker reads from a Redis Stream
-- consumer group and XACKs only after a successful INSERT. If an INSERT
-- succeeds but the process crashes before XACK, the message will be
-- redelivered and inserted again — so duplicates are possible.
--
-- Day 6 aggregation must be idempotent (e.g. dedupe on a natural key or
-- aggregate in a way that tolerates repeats). Do NOT add dedup logic here.

CREATE TABLE IF NOT EXISTS raw_events (
  id          BIGSERIAL PRIMARY KEY,
  key         TEXT NOT NULL,
  algorithm   TEXT NOT NULL,
  allowed     BOOLEAN NOT NULL,
  status      INTEGER NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS raw_events_key_created_at_idx
  ON raw_events (key, created_at);

-- aggregated_metrics is a derived rollup of raw_events into per-minute buckets.
--
-- IDEMPOTENCY CONTRACT — read before changing the aggregator:
--   The aggregator recomputes counts from raw_events each pass and UPSERTs
--   absolute values (SET count = EXCLUDED.count). Do NOT change this to an
--   increment (SET count = count + EXCLUDED.count) — that would double-count
--   on every overlapping pass and break restart safety. The UNIQUE constraint
--   below is what makes the upsert work; keep it aligned with the GROUP BY.

CREATE TABLE IF NOT EXISTS aggregated_metrics (
  key             TEXT NOT NULL,
  algorithm       TEXT NOT NULL,
  bucket_start    TIMESTAMPTZ NOT NULL,
  allowed_count   BIGINT NOT NULL,
  rejected_count  BIGINT NOT NULL,
  total           BIGINT NOT NULL,
  UNIQUE (key, algorithm, bucket_start)
);

-- Supports "last N minutes for key=X" queries (Day 7 read API, Day 8 dashboard).
CREATE INDEX IF NOT EXISTS aggregated_metrics_key_bucket_idx
  ON aggregated_metrics (key, bucket_start DESC);

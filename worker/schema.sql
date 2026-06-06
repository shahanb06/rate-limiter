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

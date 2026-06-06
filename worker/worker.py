"""Analytics worker: drains rl:events from Redis into Postgres raw_events,
and periodically rolls raw_events up into aggregated_metrics.

Delivery semantics: at-least-once. We XACK only after a successful INSERT
batch commits. If the process crashes between INSERT commit and XACK, the
message is redelivered and inserted again — raw_events may contain duplicates.

Aggregation idempotency: each pass recomputes per-minute counts from
raw_events and UPSERTs absolute values (SET count = EXCLUDED.count). Do NOT
change this to an increment — restart safety and overlap tolerance both
depend on the recount-and-overwrite shape. See schema.sql for the contract.
"""

import logging
import os
import signal
import sys
import threading
import time
from datetime import datetime, timedelta, timezone
from pathlib import Path

import psycopg
import redis

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(message)s",
)
log = logging.getLogger("worker")

REDIS_URL = os.environ.get("REDIS_URL", "redis://localhost:6379")
DATABASE_URL = os.environ.get(
    "DATABASE_URL",
    "postgresql://ratelimiter:ratelimiter@localhost:5432/ratelimiter",
)
STREAM = os.environ.get("STREAM", "rl:events")
GROUP = os.environ.get("GROUP", "rl-workers")
CONSUMER = os.environ.get("CONSUMER", "worker-1")
BATCH_SIZE = int(os.environ.get("BATCH_SIZE", "100"))
BLOCK_MS = int(os.environ.get("BLOCK_MS", "5000"))

AGG_INTERVAL_S = int(os.environ.get("AGG_INTERVAL_S", "15"))
AGG_LOOKBACK_S = int(os.environ.get("AGG_LOOKBACK_S", "300"))      # 5 min overlap
AGG_COLD_START_S = int(os.environ.get("AGG_COLD_START_S", "3600")) # 1 hour on empty table
SCHEMA_PATH = Path(os.environ.get("SCHEMA_PATH", "schema.sql"))

_stop = False


def _handle_signal(signum, _frame):
    global _stop
    log.info("received signal %s, shutting down", signum)
    _stop = True


def connect_redis() -> redis.Redis:
    r = redis.from_url(REDIS_URL, decode_responses=True)
    r.ping()
    return r


def connect_postgres() -> psycopg.Connection:
    # Compose ordering races with healthchecks occasionally; retry briefly.
    last_err = None
    for attempt in range(1, 11):
        try:
            conn = psycopg.connect(DATABASE_URL, autocommit=False)
            return conn
        except Exception as e:  # noqa: BLE001
            last_err = e
            log.warning("postgres connect attempt %d failed: %s", attempt, e)
            time.sleep(1)
    raise RuntimeError(f"postgres unreachable: {last_err}")


def ensure_group(r: redis.Redis) -> None:
    try:
        r.xgroup_create(STREAM, GROUP, id="$", mkstream=True)
        log.info("created consumer group %s on %s", GROUP, STREAM)
    except redis.ResponseError as e:
        if "BUSYGROUP" in str(e):
            log.info("consumer group %s already exists", GROUP)
        else:
            raise


def parse_event(fields: dict) -> tuple:
    ts_ms = int(fields["ts"])
    created_at = datetime.fromtimestamp(ts_ms / 1000.0, tz=timezone.utc)
    return (
        fields["key"],
        fields["algorithm"],
        fields["allowed"] == "1",
        int(fields["status"]),
        created_at,
    )


def process_batch(conn: psycopg.Connection, entries: list) -> list:
    """INSERT all entries in one transaction. Returns the list of stream IDs
    to XACK on success. Raises on failure (caller should not XACK)."""
    rows = [parse_event(fields) for _id, fields in entries]
    ids = [eid for eid, _ in entries]

    with conn.cursor() as cur:
        cur.executemany(
            "INSERT INTO raw_events (key, algorithm, allowed, status, created_at) "
            "VALUES (%s, %s, %s, %s, %s)",
            rows,
        )
    conn.commit()
    return ids


def ensure_schema(conn: psycopg.Connection) -> None:
    """Apply schema.sql idempotently. Postgres's init dir only runs on first
    boot of a fresh volume; this lets the worker bootstrap a running DB."""
    sql = SCHEMA_PATH.read_text()
    with conn.cursor() as cur:
        cur.execute(sql)
    conn.commit()
    log.info("schema applied from %s", SCHEMA_PATH)


# Aggregation query. Reads raw_events (which may contain duplicates per
# at-least-once delivery), groups by per-minute bucket, and UPSERTs absolute
# counts. Re-running over the same window produces identical row state.
_AGG_SQL = """
INSERT INTO aggregated_metrics (
  key, algorithm, bucket_start, allowed_count, rejected_count, total
)
SELECT
  key,
  algorithm,
  date_trunc('minute', created_at) AS bucket_start,
  count(*) FILTER (WHERE allowed)     AS allowed_count,
  count(*) FILTER (WHERE NOT allowed) AS rejected_count,
  count(*)                            AS total
FROM raw_events
WHERE created_at >= %s
GROUP BY key, algorithm, bucket_start
ON CONFLICT (key, algorithm, bucket_start) DO UPDATE
  SET allowed_count  = EXCLUDED.allowed_count,
      rejected_count = EXCLUDED.rejected_count,
      total          = EXCLUDED.total
RETURNING bucket_start
"""


class Aggregator(threading.Thread):
    """Periodic per-minute rollup of raw_events into aggregated_metrics.

    Owns its own psycopg connection — psycopg connections are not safe to
    share across threads with the consumer loop.
    """

    def __init__(self) -> None:
        super().__init__(name="aggregator", daemon=False)
        self._last_bucket: datetime | None = None

    def _init_watermark(self, conn: psycopg.Connection) -> datetime:
        with conn.cursor() as cur:
            cur.execute("SELECT MAX(bucket_start) FROM aggregated_metrics")
            row = cur.fetchone()
        if row and row[0] is not None:
            return row[0]
        return datetime.now(timezone.utc) - timedelta(seconds=AGG_COLD_START_S)

    def _run_pass(self, conn: psycopg.Connection) -> int:
        # Scan window: watermark minus lookback, so late-arriving raw rows in
        # already-aggregated buckets get picked up. Overlap is harmless because
        # the UPSERT writes absolute values (see schema.sql idempotency contract).
        assert self._last_bucket is not None
        scan_from = self._last_bucket - timedelta(seconds=AGG_LOOKBACK_S)

        with conn.cursor() as cur:
            cur.execute(_AGG_SQL, (scan_from,))
            buckets = [row[0] for row in cur.fetchall()]
        conn.commit()

        if buckets:
            self._last_bucket = max(buckets)
        return len(buckets)

    def run(self) -> None:
        try:
            conn = connect_postgres()
        except Exception as e:  # noqa: BLE001
            log.error("aggregator: postgres connect failed: %s", e)
            return

        try:
            self._last_bucket = self._init_watermark(conn)
            log.info("aggregator started watermark=%s", self._last_bucket.isoformat())

            while not _stop:
                try:
                    n = self._run_pass(conn)
                    log.info(
                        "aggregator pass buckets=%d watermark=%s",
                        n, self._last_bucket.isoformat(),
                    )
                except Exception as e:  # noqa: BLE001
                    log.error("aggregator pass failed: %s", e)
                    conn.rollback()

                # Sleep AGG_INTERVAL_S in 0.5s slices so SIGTERM exits promptly.
                slept = 0.0
                while slept < AGG_INTERVAL_S and not _stop:
                    time.sleep(0.5)
                    slept += 0.5
        finally:
            conn.close()
            log.info("aggregator stopped")


def main() -> int:
    signal.signal(signal.SIGTERM, _handle_signal)
    signal.signal(signal.SIGINT, _handle_signal)

    log.info("starting worker stream=%s group=%s consumer=%s", STREAM, GROUP, CONSUMER)

    r = connect_redis()
    conn = connect_postgres()
    ensure_schema(conn)
    ensure_group(r)

    aggregator = Aggregator()
    aggregator.start()

    while not _stop:
        try:
            resp = r.xreadgroup(
                GROUP,
                CONSUMER,
                streams={STREAM: ">"},
                count=BATCH_SIZE,
                block=BLOCK_MS,
            )
        except redis.ConnectionError as e:
            log.warning("redis read error: %s", e)
            time.sleep(1)
            continue

        if not resp:
            continue  # block timeout, loop and check _stop

        for _stream_name, entries in resp:
            if not entries:
                continue
            try:
                ids = process_batch(conn, entries)
            except Exception as e:  # noqa: BLE001
                log.error("insert failed for %d events: %s", len(entries), e)
                conn.rollback()
                # Do not XACK — messages will be redelivered.
                time.sleep(0.5)
                continue

            try:
                r.xack(STREAM, GROUP, *ids)
                log.info("processed batch size=%d", len(ids))
            except redis.ConnectionError as e:
                log.warning("xack failed: %s (will redeliver)", e)

    aggregator.join(timeout=AGG_INTERVAL_S + 5)
    if aggregator.is_alive():
        log.warning("aggregator did not exit within timeout")

    log.info("worker stopped")
    conn.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())

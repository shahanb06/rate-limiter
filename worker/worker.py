"""Analytics worker: drains rl:events from Redis into Postgres raw_events.

Delivery semantics: at-least-once. We XACK only after a successful INSERT
batch commits. If the process crashes between INSERT commit and XACK, the
message is redelivered and inserted again. Day 6 aggregation must be
idempotent — do not add dedup logic here.
"""

import logging
import os
import signal
import sys
import time
from datetime import datetime, timezone

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


def main() -> int:
    signal.signal(signal.SIGTERM, _handle_signal)
    signal.signal(signal.SIGINT, _handle_signal)

    log.info("starting worker stream=%s group=%s consumer=%s", STREAM, GROUP, CONSUMER)

    r = connect_redis()
    conn = connect_postgres()
    ensure_group(r)

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

    log.info("worker stopped")
    conn.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())

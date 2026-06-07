"""Aggregator idempotency tests against a real Postgres.

The Day 6 contract — "INSERT ... ON CONFLICT (key, algorithm, bucket_start)
DO UPDATE SET allowed_count = EXCLUDED.allowed_count, ..." — is the marquee
claim of the aggregation pipeline. It is what makes the worker restart-safe
and tolerant of at-least-once duplicates in raw_events.

That guarantee is entirely DB-side. A mocked psycopg.Connection or an
extracted "pure aggregator" function would not exercise the UPSERT, so we
spin up a throwaway Postgres via testcontainers and drive Aggregator._run_pass
end-to-end. No SQL is reimplemented in the test — we call the real code path.

Run from worker/:
    python3 -m unittest test_aggregator -v
"""
import unittest
from datetime import datetime, timedelta, timezone

import psycopg
from testcontainers.postgres import PostgresContainer

# Importing worker triggers `import redis` at module top — redis must be
# installed for these tests to import (see requirements-dev.txt). None of
# the tests touch Redis; the import is just because worker.py is the real
# production module we are exercising.
from worker import Aggregator, ensure_schema


# Module-scoped container. Spinning up Postgres costs ~3-5s; sharing one
# across the four tests amortizes that cost. We TRUNCATE between tests
# instead of restarting the container.
_pg: PostgresContainer | None = None
_url: str | None = None


def setUpModule():
    global _pg, _url
    _pg = PostgresContainer("postgres:16-alpine")
    _pg.start()
    # testcontainers v4 returns a SQLAlchemy-style URL with a +driver suffix.
    # psycopg wants the bare postgresql:// form.
    raw = _pg.get_connection_url()
    _url = raw.replace("postgresql+psycopg2://", "postgresql://", 1)


def tearDownModule():
    if _pg is not None:
        _pg.stop()


def _connect() -> psycopg.Connection:
    return psycopg.connect(_url, autocommit=False)


def _insert_raw_event(conn, key, algo, allowed, status, ts):
    """Insert one raw_events row. This is the only SQL the test writes that
    is not the real aggregator's. Reading is also bespoke; the aggregator
    write path (_AGG_SQL) is exercised exclusively via Aggregator._run_pass."""
    with conn.cursor() as cur:
        cur.execute(
            "INSERT INTO raw_events (key, algorithm, allowed, status, created_at) "
            "VALUES (%s, %s, %s, %s, %s)",
            (key, algo, allowed, status, ts),
        )
    conn.commit()


def _read_bucket(conn, key, algo, bucket_start):
    with conn.cursor() as cur:
        cur.execute(
            "SELECT allowed_count, rejected_count, total "
            "FROM aggregated_metrics "
            "WHERE key=%s AND algorithm=%s AND bucket_start=%s",
            (key, algo, bucket_start),
        )
        return cur.fetchone()


class AggregatorIdempotencyTests(unittest.TestCase):
    """Drive the real Aggregator._run_pass against a throwaway Postgres."""

    KEY = "tenant-a"
    ALGO = "fixed"
    # Fixed minute boundary so date_trunc('minute', ...) is deterministic
    # and no test ever straddles a minute change.
    BUCKET = datetime(2026, 6, 7, 12, 0, 0, tzinfo=timezone.utc)

    def setUp(self):
        self.conn = _connect()
        ensure_schema(self.conn)
        # Fresh state between tests: the aggregator scans ALL of raw_events
        # at or after its watermark-minus-lookback cutoff, so leftover rows
        # from prior tests would corrupt counts.
        with self.conn.cursor() as cur:
            cur.execute("TRUNCATE raw_events, aggregated_metrics")
        self.conn.commit()

        # Build an Aggregator and seed its watermark just before our test
        # bucket so the scan window covers it. We bypass _init_watermark
        # (which would fall back to AGG_COLD_START_S on an empty table).
        self.agg = Aggregator()
        self.agg._last_bucket = self.BUCKET - timedelta(minutes=1)

    def tearDown(self):
        self.conn.close()

    def _seed_window(self, *, n_allowed: int, n_rejected: int):
        """Insert n_allowed + n_rejected raw events all inside self.BUCKET."""
        # Spread across distinct seconds so we have unique timestamps but
        # all land in the same minute bucket.
        for i in range(n_allowed):
            _insert_raw_event(
                self.conn, self.KEY, self.ALGO, True, 200,
                self.BUCKET + timedelta(seconds=i),
            )
        for i in range(n_rejected):
            _insert_raw_event(
                self.conn, self.KEY, self.ALGO, False, 429,
                self.BUCKET + timedelta(seconds=30 + i),
            )

    # (1) Basic aggregation.
    def test_basic_aggregation(self):
        self._seed_window(n_allowed=7, n_rejected=3)
        n = self.agg._run_pass(self.conn)
        self.assertGreaterEqual(n, 1, "pass should have UPSERTed at least one bucket")
        row = _read_bucket(self.conn, self.KEY, self.ALGO, self.BUCKET)
        self.assertEqual(row, (7, 3, 10))

    # (2) IDEMPOTENCY — at-least-once duplicates do NOT double-count.
    # This is the central proof. If anyone changes _AGG_SQL from
    #   SET allowed_count = EXCLUDED.allowed_count
    # to the increment form
    #   SET allowed_count = aggregated_metrics.allowed_count + EXCLUDED.allowed_count
    # this test fails loudly.
    def test_duplicates_do_not_double_count(self):
        self._seed_window(n_allowed=7, n_rejected=3)
        self.agg._run_pass(self.conn)
        first = _read_bucket(self.conn, self.KEY, self.ALGO, self.BUCKET)
        self.assertEqual(first, (7, 3, 10))

        # Simulate at-least-once redelivery: same logical events inserted
        # again. raw_events now contains literal duplicates — which is the
        # documented possibility per schema.sql's at-least-once contract.
        self._seed_window(n_allowed=7, n_rejected=3)
        with self.conn.cursor() as cur:
            cur.execute("SELECT count(*) FROM raw_events")
            (raw_count,) = cur.fetchone()
        self.assertEqual(raw_count, 20, "raw_events should hold duplicates now")

        # Re-aggregate. The recount-and-overwrite contract means the
        # aggregate equals the recount over the CURRENT raw_events state.
        # Two copies of each event -> 14 allowed + 6 rejected = 20 total.
        self.agg._run_pass(self.conn)
        second = _read_bucket(self.conn, self.KEY, self.ALGO, self.BUCKET)
        self.assertEqual(second, (14, 6, 20))

        # The anti-claim that this test exists to catch: a naive
        # "count = count + EXCLUDED.count" UPSERT would have produced
        # (7+14, 3+6, 10+20) = (21, 9, 30) on the second pass. If you ever
        # see that here, the schema's idempotency contract has been broken.
        self.assertNotEqual(
            second, (21, 9, 30),
            "increment-on-conflict would double-count; the contract is "
            "recount-and-overwrite (see schema.sql)",
        )

    # (3) Re-aggregation stability: passes over unchanged raw_events are
    # no-ops at the aggregate level.
    def test_reaggregation_stability(self):
        self._seed_window(n_allowed=7, n_rejected=3)
        self.agg._run_pass(self.conn)
        baseline = _read_bucket(self.conn, self.KEY, self.ALGO, self.BUCKET)

        for _ in range(3):
            self.agg._run_pass(self.conn)
        after = _read_bucket(self.conn, self.KEY, self.ALGO, self.BUCKET)
        self.assertEqual(after, baseline)

    # (4) Late-arriving row in an already-aggregated bucket. The
    # watermark-minus-lookback scan (AGG_LOOKBACK_S = 300s by default) is
    # what gives us a window to re-pick up these stragglers.
    def test_late_arriving_row_picked_up_by_lookback(self):
        self._seed_window(n_allowed=7, n_rejected=3)
        self.agg._run_pass(self.conn)
        before = _read_bucket(self.conn, self.KEY, self.ALGO, self.BUCKET)
        self.assertEqual(before, (7, 3, 10))

        # One late allowed event arrives in the same bucket. After the next
        # pass, the lookback re-scans this bucket and the UPSERT writes the
        # new recount (8 allowed instead of 7).
        _insert_raw_event(
            self.conn, self.KEY, self.ALGO, True, 200,
            self.BUCKET + timedelta(seconds=42),
        )
        self.agg._run_pass(self.conn)
        after = _read_bucket(self.conn, self.KEY, self.ALGO, self.BUCKET)
        self.assertEqual(after, (8, 3, 11))


if __name__ == "__main__":
    unittest.main()

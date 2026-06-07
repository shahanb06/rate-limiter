"""Worker alert tests.

The five numbered scenarios from the Day 10 spec are exercised against a real
local HTTP listener that captures POST bodies, so we verify both the
decision logic and the urllib delivery path. A short set of unit tests for
decide_alert covers the pure-function semantics directly.

Run from the worker/ directory:
    python3 -m unittest test_alerts -v
"""
import http.server
import json
import threading
import unittest

from alerts import decide_alert, evaluate_alerts, send_webhook


class _CaptureHandler(http.server.BaseHTTPRequestHandler):
    posts: list = []

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length).decode("utf-8")
        _CaptureHandler.posts.append(json.loads(body))
        self.send_response(200)
        self.end_headers()

    def log_message(self, *args, **kwargs):
        return  # silence test output


class AlertWebhookTests(unittest.TestCase):
    """End-to-end: drive evaluate_alerts + send_webhook against a real
    local HTTPServer and assert on the bodies it captured."""

    THRESHOLD = 0.5
    MIN_VOLUME = 20
    COOLDOWN = 300
    WINDOW_MINUTES = 5

    def setUp(self):
        _CaptureHandler.posts = []
        # Port 0 = OS picks a free port.
        self.srv = http.server.HTTPServer(("127.0.0.1", 0), _CaptureHandler)
        self.thread = threading.Thread(target=self.srv.serve_forever, daemon=True)
        self.thread.start()
        self.url = f"http://127.0.0.1:{self.srv.server_port}/"

    def tearDown(self):
        self.srv.shutdown()
        self.srv.server_close()
        self.thread.join(timeout=2)

    def _tick(self, rows, alert_state, now):
        """One aggregator-pass-equivalent: evaluate alerts on the given rows,
        deliver every produced payload via send_webhook."""
        payloads = evaluate_alerts(
            rows, now, alert_state,
            threshold=self.THRESHOLD,
            min_volume=self.MIN_VOLUME,
            cooldown_s=self.COOLDOWN,
            window_minutes=self.WINDOW_MINUTES,
        )
        for p in payloads:
            ok = send_webhook(self.url, p, timeout=2.0)
            self.assertTrue(ok, "webhook send returned False against local fake server")

    # (1) fires on a clear breach above threshold + above volume floor.
    def test_fires_on_clear_breach(self):
        state: dict = {}
        self._tick([("alpha", 100, 60)], state, now=1000.0)
        self.assertEqual(len(_CaptureHandler.posts), 1)
        body = _CaptureHandler.posts[0]
        self.assertEqual(body["key"], "alpha")
        self.assertAlmostEqual(body["rejection_rate"], 0.6, places=6)
        self.assertEqual(body["total"], 100)
        self.assertEqual(body["rejected"], 60)
        self.assertEqual(body["window_minutes"], 5)
        self.assertEqual(body["threshold"], 0.5)
        self.assertIn("alpha", body["text"])
        # Verify the text field works for Slack-style incoming webhooks.
        self.assertIn("rejection_rate=60.0%", body["text"])
        self.assertEqual(state["alpha"]["state"], "breached")
        self.assertEqual(state["alpha"]["last_alert"], 1000.0)

    # (2) does NOT fire when rate is under threshold.
    def test_no_fire_under_threshold(self):
        state: dict = {}
        self._tick([("alpha", 100, 30)], state, now=1000.0)
        self.assertEqual(len(_CaptureHandler.posts), 0)
        self.assertEqual(state["alpha"]["state"], "ok")
        self.assertIsNone(state["alpha"]["last_alert"])

    # (3) does NOT fire when total is under ALERT_MIN_VOLUME even at 100%.
    def test_no_fire_low_volume_even_at_100pct(self):
        state: dict = {}
        self._tick([("alpha", 5, 5)], state, now=1000.0)
        self.assertEqual(len(_CaptureHandler.posts), 0)
        # Low-volume signal must not transition state either.
        self.assertEqual(state["alpha"]["state"], "ok")

    # (4) does NOT re-fire within cooldown. Covers both:
    #   - still-breaching subsequent pass (edge-triggered)
    #   - drop-then-rebreach inside the cooldown window (cooldown check)
    def test_no_refire_within_cooldown(self):
        state: dict = {}
        # Initial breach: fires.
        self._tick([("alpha", 100, 60)], state, now=1000.0)
        self.assertEqual(len(_CaptureHandler.posts), 1)
        # Still breaching one tick later: edge-triggered, no fire.
        self._tick([("alpha", 100, 60)], state, now=1100.0)
        self.assertEqual(len(_CaptureHandler.posts), 1)
        # Brief drop clears state -> "ok" but last_alert stays at 1000.0.
        self._tick([("alpha", 100, 10)], state, now=1150.0)
        self.assertEqual(state["alpha"]["state"], "ok")
        self.assertEqual(state["alpha"]["last_alert"], 1000.0)
        # Re-breach at t=1200 (only 200s after the alert; cooldown=300s).
        # Even though it's a fresh ok->breach transition, cooldown suppresses.
        self._tick([("alpha", 100, 60)], state, now=1200.0)
        self.assertEqual(len(_CaptureHandler.posts), 1)

    # (5) fires again after dropping to ok then breaching past cooldown.
    def test_fires_again_after_drop_and_rebreach(self):
        state: dict = {}
        self._tick([("alpha", 100, 60)], state, now=1000.0)
        self.assertEqual(len(_CaptureHandler.posts), 1)
        # Drop clears the breach.
        self._tick([("alpha", 100, 10)], state, now=1100.0)
        self.assertEqual(state["alpha"]["state"], "ok")
        # Re-breach past cooldown (1400 - 1000 = 400 > 300) — fires.
        self._tick([("alpha", 100, 60)], state, now=1400.0)
        self.assertEqual(len(_CaptureHandler.posts), 2)
        self.assertEqual(state["alpha"]["state"], "breached")
        self.assertEqual(state["alpha"]["last_alert"], 1400.0)


class DecideAlertUnitTests(unittest.TestCase):
    """Pure-function checks on decide_alert with no HTTP at all."""

    def _decide(self, **overrides):
        defaults = dict(
            rate=0.6, total=100, prior_state="ok", last_alert_ts=None, now=0.0,
            threshold=0.5, min_volume=20, cooldown_s=300,
        )
        defaults.update(overrides)
        return decide_alert(**defaults)

    def test_low_volume_does_not_transition(self):
        self.assertEqual(self._decide(rate=1.0, total=3), ("ok", False))
        # Even if prior was breached, low-volume leaves state untouched.
        self.assertEqual(
            self._decide(rate=1.0, total=3, prior_state="breached"),
            ("breached", False),
        )

    def test_ok_to_breached_fires(self):
        self.assertEqual(self._decide(), ("breached", True))

    def test_breached_no_refire_while_over(self):
        self.assertEqual(
            self._decide(prior_state="breached", last_alert_ts=0.0, now=10.0),
            ("breached", False),
        )

    def test_breached_drops_to_ok_below_threshold(self):
        self.assertEqual(
            self._decide(rate=0.1, prior_state="breached", last_alert_ts=0.0, now=10.0),
            ("ok", False),
        )

    def test_cooldown_blocks_fresh_transition(self):
        # 200s after last alert, cooldown 300s -> suppressed, stays "ok"
        # so the next pass can fire once cooldown elapses.
        self.assertEqual(
            self._decide(last_alert_ts=100.0, now=200.0),
            ("ok", False),
        )


if __name__ == "__main__":
    unittest.main()

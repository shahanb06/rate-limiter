"""Rejection-rate alerting for the analytics worker.

Pure decision logic + HTTP delivery only — no DB, no Redis. The aggregator
queries aggregated_metrics for the window and feeds the rows into
evaluate_alerts(); the alert logic itself never touches the data layer.
Tests can exercise this module without psycopg or redis installed.

State model per key:
- "ok"        : not currently considered breached.
- "breached"  : an alert was sent and the rate has not yet dropped below
                threshold. No re-fire while in this state — edge-triggered.

A drop below threshold resets to "ok" so a later breach can fire again.
Low-volume windows neither fire nor change state (avoids flapping on quiet
keys). Cooldown caps the alert frequency on pathological flap cycles.

In-memory state resets on worker restart: a still-breaching key may re-fire
once on cold start, which cooldown bounds to one alert per cooldown window —
acceptable for v1.
"""
import json
import logging
import urllib.error
import urllib.request
from typing import Iterable, Optional

log = logging.getLogger("worker.alerts")


def decide_alert(
    *,
    rate: float,
    total: int,
    prior_state: str,
    last_alert_ts: Optional[float],
    now: float,
    threshold: float,
    min_volume: int,
    cooldown_s: float,
) -> tuple[str, bool]:
    """Pure: given current (rate, total) for a key, its prior alert state,
    and config, return (new_state, should_fire).

    The whole alert behavior lives here so it's unit-testable without a real
    DB or network. Inputs are explicit; nothing reads from globals.
    """
    if total < min_volume:
        # Low-volume signal is unreliable. Don't fire and don't transition —
        # leaving state untouched avoids spurious ok<->breached churn on keys
        # that briefly fall quiet between windows.
        return prior_state, False

    if rate < threshold:
        # Below threshold clears any prior breach.
        return "ok", False

    if prior_state == "breached":
        # Already alerted, still over — edge-triggered, no re-fire.
        return "breached", False

    # ok -> breaching transition. Cooldown protects against rapid flap
    # (ok -> breached -> ok -> breached) firing repeatedly in a short window.
    within_cooldown = (
        last_alert_ts is not None and (now - last_alert_ts) < cooldown_s
    )
    if within_cooldown:
        # Suppress this transition; stay "ok" so we'll re-evaluate next pass
        # and fire then once cooldown has elapsed (if still breaching).
        return "ok", False
    return "breached", True


def build_payload(
    *,
    key: str,
    rate: float,
    total: int,
    rejected: int,
    now: float,
    threshold: float,
    window_minutes: int,
) -> dict:
    return {
        "text": (
            f"Rate limiter alert: key={key} "
            f"rejection_rate={rate*100:.1f}% "
            f"over last {window_minutes}m (total={total})"
        ),
        "key": key,
        "rejection_rate": rate,
        "total": total,
        "rejected": rejected,
        "window_minutes": window_minutes,
        "threshold": threshold,
        "ts": now,
    }


def send_webhook(url: str, payload: dict, timeout: float = 3.0) -> bool:
    """POST JSON to url. Returns True on success, False on any failure.
    Never raises — failures must not propagate into the aggregation loop or
    affect consumer XACK. The url is read from env and treated as a secret;
    we log only the exception type, never the URL or full exception args.
    """
    body = json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(
        url,
        data=body,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            resp.read()
        return True
    except Exception as e:  # noqa: BLE001 - by design: never crash callers
        log.warning("webhook delivery failed: %s", type(e).__name__)
        return False


def evaluate_alerts(
    rows: Iterable[tuple[str, int, int]],
    now: float,
    alert_state: dict[str, dict],
    *,
    threshold: float,
    min_volume: int,
    cooldown_s: float,
    window_minutes: int,
) -> list[dict]:
    """Drive decide_alert across all keys in this window. Mutates
    alert_state in place; returns the list of payloads the caller should
    deliver via send_webhook.

    Each row is (key, total, rejected) summed over the alert window. The
    caller is responsible for the SQL query that produces these rows so
    this function stays free of DB dependencies.
    """
    payloads: list[dict] = []
    for key, total, rejected in rows:
        rate = (rejected / total) if total > 0 else 0.0
        prior = alert_state.get(key, {"state": "ok", "last_alert": None})
        new_state, fire = decide_alert(
            rate=rate,
            total=total,
            prior_state=prior["state"],
            last_alert_ts=prior["last_alert"],
            now=now,
            threshold=threshold,
            min_volume=min_volume,
            cooldown_s=cooldown_s,
        )
        alert_state[key] = {
            "state": new_state,
            "last_alert": now if fire else prior["last_alert"],
        }
        if fire:
            payloads.append(build_payload(
                key=key,
                rate=rate,
                total=total,
                rejected=rejected,
                now=now,
                threshold=threshold,
                window_minutes=window_minutes,
            ))
    return payloads

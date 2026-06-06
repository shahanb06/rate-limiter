"use client";

import { useEffect, useState } from "react";
import KeyPicker from "./components/KeyPicker";
import StatusBanner from "./components/StatusBanner";
import SummaryCard from "./components/SummaryCard";
import TimeseriesChart from "./components/TimeseriesChart";
import {
  API_BASE_URL,
  getKeys,
  getSummary,
  getTimeseries,
  type SummaryResp,
  type TimeseriesPoint,
} from "./lib/api";

const POLL_MS = 7000;
const SINCE = "1h";

export default function Dashboard() {
  const [keys, setKeys] = useState<string[]>([]);
  const [keysErr, setKeysErr] = useState<string | null>(null);
  const [keysLoading, setKeysLoading] = useState(true);

  const [selected, setSelected] = useState<string | null>(null);
  const [summary, setSummary] = useState<SummaryResp | null>(null);
  const [points, setPoints] = useState<TimeseriesPoint[]>([]);
  const [pollErr, setPollErr] = useState<string | null>(null);

  // One-shot: load the key list on mount.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      const res = await getKeys();
      if (cancelled) return;
      if (!res.ok) {
        setKeysErr(res.error);
      } else {
        setKeys(res.data.keys);
        if (res.data.keys.length > 0) setSelected(res.data.keys[0]);
      }
      setKeysLoading(false);
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  // Poll: fetch summary + timeseries every POLL_MS while a key is selected.
  // The `cancelled` flag prevents a request started under key A from writing
  // stale data into the panels after the user switches to key B.
  useEffect(() => {
    if (!selected) return;

    let cancelled = false;

    const tick = async () => {
      const [s, ts] = await Promise.all([
        getSummary(selected),
        getTimeseries(selected, SINCE),
      ]);
      if (cancelled) return;

      if (!s.ok || !ts.ok) {
        setPollErr(!s.ok ? s.error : !ts.ok ? ts.error : null);
        return;
      }
      setSummary(s.data);
      setPoints(ts.data.points);
      setPollErr(null);
    };

    // Reset the panels on key-change so we don't show the previous key's
    // numbers while the first poll is in flight.
    setSummary(null);
    setPoints([]);
    setPollErr(null);

    tick();
    const id = setInterval(tick, POLL_MS);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, [selected]);

  return (
    <main className="mx-auto max-w-5xl px-6 py-10 space-y-6">
      <header className="flex items-baseline justify-between">
        <div>
          <h1 className="text-2xl font-semibold">Rate Limiter Dashboard</h1>
          <p className="text-sm text-slate-500">
            Live analytics · polling every {POLL_MS / 1000}s · window: last {SINCE}
          </p>
        </div>
        <KeyPicker
          keys={keys}
          selected={selected}
          onSelect={setSelected}
          disabled={keysLoading}
        />
      </header>

      {keysErr && (
        <StatusBanner tone="error">
          Couldn&apos;t reach the API at <code className="text-rose-300">{API_BASE_URL}</code>:{" "}
          {keysErr}. Is the Go service running?
        </StatusBanner>
      )}

      {!keysErr && !keysLoading && keys.length === 0 && (
        <StatusBanner tone="warn">
          No keys yet. Send some <code>POST /check</code> traffic and wait ~15s for the
          aggregator to flush a bucket.
        </StatusBanner>
      )}

      {selected && pollErr && (
        <StatusBanner tone="warn">
          Refresh failed: {pollErr}. Retrying every {POLL_MS / 1000}s — last good data shown
          below.
        </StatusBanner>
      )}

      {selected && (
        <>
          <SummaryCard summary={summary} />
          <TimeseriesChart points={points} />
        </>
      )}
    </main>
  );
}

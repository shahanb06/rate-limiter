"use client";

import { useEffect, useState } from "react";
import AlgorithmBreakdown from "./components/AlgorithmBreakdown";
import KeyPicker from "./components/KeyPicker";
import LeaderboardTable from "./components/LeaderboardTable";
import RejectionGauge from "./components/RejectionGauge";
import RejectionRateChart from "./components/RejectionRateChart";
import StatusBanner from "./components/StatusBanner";
import SummaryCard from "./components/SummaryCard";
import TimeseriesChart from "./components/TimeseriesChart";
import {
  API_BASE_URL,
  getKeys,
  getLeaderboard,
  getSummary,
  getSummaryByAlgorithm,
  getTimeseries,
  type LeaderboardRow,
  type SummaryByAlgoRow,
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
  const [byAlgo, setByAlgo] = useState<SummaryByAlgoRow[] | null>(null);
  const [leaderboard, setLeaderboard] = useState<LeaderboardRow[] | null>(null);
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

  // Single polling effect — all live fetches share one 7s interval, one
  // cancelled flag, and one clearInterval cleanup. The leaderboard refreshes
  // every tick regardless of selection; the per-key panels only fetch when a
  // key is selected. Promise.all keeps all five requests in flight together.
  useEffect(() => {
    let cancelled = false;

    const tick = async () => {
      const [lb, s, ts, alg] = await Promise.all([
        getLeaderboard("24h"),
        selected ? getSummary(selected) : Promise.resolve(null),
        selected ? getTimeseries(selected, SINCE) : Promise.resolve(null),
        selected ? getSummaryByAlgorithm(selected) : Promise.resolve(null),
      ]);
      if (cancelled) return;

      // Surface the first error we see. If the leaderboard fails but the
      // per-key fetches succeed (or vice versa), we still update the panels
      // that did work — last-good-data semantics, same as Day 8.
      let nextErr: string | null = null;

      if (!lb.ok) {
        nextErr = lb.error;
      } else {
        setLeaderboard(lb.data.rows);
      }

      if (s) {
        if (!s.ok) nextErr ??= s.error;
        else setSummary(s.data);
      }
      if (ts) {
        if (!ts.ok) nextErr ??= ts.error;
        else setPoints(ts.data.points);
      }
      if (alg) {
        if (!alg.ok) nextErr ??= alg.error;
        else setByAlgo(alg.data.by_algorithm);
      }

      setPollErr(nextErr);
    };

    // On key-change, reset the per-key panels so the previous key's numbers
    // don't linger while the first poll is in flight. Leaderboard is global
    // and is intentionally kept across the transition.
    setSummary(null);
    setPoints([]);
    setByAlgo(null);
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

      <LeaderboardTable
        rows={leaderboard}
        selected={selected}
        onSelect={setSelected}
      />

      {selected && (
        <>
          <SummaryCard summary={summary} points={points} />
          <RejectionGauge summary={summary} />
          <AlgorithmBreakdown rows={byAlgo} />
          <TimeseriesChart points={points} />
          <RejectionRateChart points={points} />
        </>
      )}
    </main>
  );
}

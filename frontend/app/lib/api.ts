// API client for the Go rate-limiter analytics endpoints.
// Returns a discriminated union { ok, data } | { ok: false, error } so callers
// can branch without try/catch noise.

const BASE = process.env.NEXT_PUBLIC_API_BASE_URL || "http://localhost:8080";

export type Result<T> = { ok: true; data: T } | { ok: false; error: string };

export type KeysResp = { keys: string[] };

export type SummaryResp = {
  key: string;
  allowed: number;
  rejected: number;
  total: number;
  rejection_rate: number;
};

export type TimeseriesPoint = {
  bucket_start: string;
  allowed: number;
  rejected: number;
  total: number;
};

export type TimeseriesResp = {
  key: string;
  since: string;
  points: TimeseriesPoint[];
};

export type SummaryByAlgoRow = {
  algorithm: string;
  allowed: number;
  rejected: number;
  total: number;
  rejection_rate: number;
};

export type SummaryByAlgoResp = {
  key: string;
  by_algorithm: SummaryByAlgoRow[];
};

export type LeaderboardSparklinePoint = {
  allowed: number;
  rejected: number;
  total: number;
};

export type LeaderboardRow = {
  key: string;
  allowed: number;
  rejected: number;
  total: number;
  rejection_rate: number;
  sparkline?: LeaderboardSparklinePoint[] | null;
};

export type LeaderboardResp = { rows: LeaderboardRow[] };

async function getJSON<T>(path: string, signal?: AbortSignal): Promise<Result<T>> {
  try {
    const res = await fetch(`${BASE}${path}`, { signal, cache: "no-store" });
    if (!res.ok) {
      return { ok: false, error: `${res.status} ${res.statusText}` };
    }
    const data = (await res.json()) as T;
    return { ok: true, data };
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    return { ok: false, error: msg };
  }
}

export const getKeys = (signal?: AbortSignal) =>
  getJSON<KeysResp>("/analytics/keys", signal);

export const getSummary = (key: string, signal?: AbortSignal) =>
  getJSON<SummaryResp>(`/analytics/summary?key=${encodeURIComponent(key)}`, signal);

export const getTimeseries = (key: string, since: string, signal?: AbortSignal) =>
  getJSON<TimeseriesResp>(
    `/analytics/timeseries?key=${encodeURIComponent(key)}&since=${encodeURIComponent(since)}`,
    signal,
  );

export const getSummaryByAlgorithm = (key: string, signal?: AbortSignal) =>
  getJSON<SummaryByAlgoResp>(
    `/analytics/summary?key=${encodeURIComponent(key)}&group_by=algorithm`,
    signal,
  );

export const getLeaderboard = (window?: string, signal?: AbortSignal) =>
  getJSON<LeaderboardResp>(
    window ? `/analytics/leaderboard?window=${encodeURIComponent(window)}` : `/analytics/leaderboard`,
    signal,
  );

export const API_BASE_URL = BASE;

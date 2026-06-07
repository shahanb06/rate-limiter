export function formatPct(rate: number): string {
  return `${(rate * 100).toFixed(1)}%`;
}

export function formatNumber(n: number): string {
  return n.toLocaleString();
}

export function formatTime(iso: string): string {
  const d = new Date(iso);
  return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

export type RateSeriesInput = { bucket_start: string; rejected: number; total: number };
export type RateSeriesPoint = { t: string; rate: number };

// Empty buckets (total = 0) get rate = 0, not NaN. NaN does not survive
// recharts' axis/Tooltip computations cleanly, and downstream consumers
// shouldn't have to special-case it either.
export function rejectionRateSeries(points: RateSeriesInput[]): RateSeriesPoint[] {
  return points.map((p) => ({
    t: formatTime(p.bucket_start),
    rate: p.total > 0 ? p.rejected / p.total : 0,
  }));
}

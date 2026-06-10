"use client";

import {
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import type { TimeseriesPoint } from "../lib/api";
import { formatPct, rejectionRateSeries } from "../lib/format";

type Props = { points: TimeseriesPoint[] };

export default function RejectionRateChart({ points }: Props) {
  if (points.length === 0) {
    return (
      <div className="h-64 rounded-lg border border-dashed border-slate-800 bg-[var(--surface)] p-3 flex items-center justify-center text-sm text-slate-500">
        No data in this window yet.
      </div>
    );
  }

  const data = rejectionRateSeries(points);

  // min/avg/max of the rejection rate across buckets, shown in the header.
  const rates = data.map((d) => d.rate);
  const min = Math.min(...rates);
  const max = Math.max(...rates);
  const avg = rates.reduce((a, b) => a + b, 0) / rates.length;

  return (
    <div className="h-64 rounded-lg border border-slate-800 bg-[var(--surface)] p-3">
      <div className="flex items-baseline justify-between px-1 pb-2">
        <span className="text-xs uppercase tracking-wide text-slate-500">
          Rejection rate over time
        </span>
        <span className="text-xs tabular-nums text-slate-500">
          min {formatPct(min)} · avg {formatPct(avg)} · max {formatPct(max)}
        </span>
      </div>
      <ResponsiveContainer width="100%" height="85%">
        <LineChart data={data} margin={{ top: 5, right: 20, bottom: 0, left: 0 }}>
          <CartesianGrid stroke="#1e293b" strokeDasharray="3 3" />
          <XAxis dataKey="t" stroke="#64748b" fontSize={12} />
          <YAxis
            stroke="#64748b"
            fontSize={12}
            domain={[0, 1]}
            tickFormatter={(v: number) => `${Math.round(v * 100)}%`}
          />
          <Tooltip
            contentStyle={{
              background: "#0f172a",
              border: "1px solid #334155",
              borderRadius: 6,
              fontSize: 12,
            }}
            formatter={(v: number) => [`${(v * 100).toFixed(1)}%`, "rate"]}
          />
          <Line
            type="monotone"
            dataKey="rate"
            stroke="#fbbf24"
            strokeWidth={2}
            dot={false}
          />
        </LineChart>
      </ResponsiveContainer>
    </div>
  );
}

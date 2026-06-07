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
import { formatTime } from "../lib/format";

type Props = { points: TimeseriesPoint[] };

export default function RejectionRateChart({ points }: Props) {
  if (points.length === 0) {
    return (
      <div className="h-64 rounded-lg border border-dashed border-slate-800 bg-slate-900/40 p-3 flex items-center justify-center text-sm text-slate-500">
        No data in this window yet.
      </div>
    );
  }

  // Empty buckets (total = 0) get rate = 0, not NaN. NaN does not survive
  // recharts' axis/Tooltip computations cleanly.
  const data = points.map((p) => ({
    t: formatTime(p.bucket_start),
    rate: p.total > 0 ? p.rejected / p.total : 0,
  }));

  return (
    <div className="h-64 rounded-lg border border-slate-800 bg-slate-900/60 p-3">
      <div className="px-1 pb-2 text-xs uppercase tracking-wide text-slate-500">
        Rejection rate over time
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

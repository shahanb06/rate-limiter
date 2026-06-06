"use client";

import {
  Area,
  AreaChart,
  CartesianGrid,
  Legend,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import type { TimeseriesPoint } from "../lib/api";
import { formatTime } from "../lib/format";

type Props = { points: TimeseriesPoint[] };

export default function TimeseriesChart({ points }: Props) {
  if (points.length === 0) {
    return (
      <div className="rounded-lg border border-dashed border-slate-800 bg-slate-900/40 p-12 text-center text-sm text-slate-500">
        No data in this window yet. The aggregator flushes every ~15s — send /check traffic and wait.
      </div>
    );
  }

  const data = points.map((p) => ({
    t: formatTime(p.bucket_start),
    allowed: p.allowed,
    rejected: p.rejected,
  }));

  return (
    <div className="h-72 rounded-lg border border-slate-800 bg-slate-900/60 p-3">
      <ResponsiveContainer width="100%" height="100%">
        <AreaChart data={data} margin={{ top: 10, right: 20, bottom: 0, left: 0 }}>
          <defs>
            <linearGradient id="allowedFill" x1="0" y1="0" x2="0" y2="1">
              <stop offset="5%" stopColor="#34d399" stopOpacity={0.6} />
              <stop offset="95%" stopColor="#34d399" stopOpacity={0} />
            </linearGradient>
            <linearGradient id="rejectedFill" x1="0" y1="0" x2="0" y2="1">
              <stop offset="5%" stopColor="#fb7185" stopOpacity={0.6} />
              <stop offset="95%" stopColor="#fb7185" stopOpacity={0} />
            </linearGradient>
          </defs>
          <CartesianGrid stroke="#1e293b" strokeDasharray="3 3" />
          <XAxis dataKey="t" stroke="#64748b" fontSize={12} />
          <YAxis stroke="#64748b" fontSize={12} allowDecimals={false} />
          <Tooltip
            contentStyle={{
              background: "#0f172a",
              border: "1px solid #334155",
              borderRadius: 6,
              fontSize: 12,
            }}
          />
          <Legend wrapperStyle={{ fontSize: 12 }} />
          <Area
            type="monotone"
            dataKey="allowed"
            stroke="#34d399"
            strokeWidth={2}
            fill="url(#allowedFill)"
          />
          <Area
            type="monotone"
            dataKey="rejected"
            stroke="#fb7185"
            strokeWidth={2}
            fill="url(#rejectedFill)"
          />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  );
}

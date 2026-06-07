"use client";

import {
  Bar,
  BarChart,
  CartesianGrid,
  Legend,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import type { SummaryByAlgoRow } from "../lib/api";

type Props = { rows: SummaryByAlgoRow[] | null };

export default function AlgorithmBreakdown({ rows }: Props) {
  if (rows === null) {
    return (
      <div className="h-64 rounded-lg border border-slate-800 bg-slate-900/60 p-3 flex items-center justify-center text-sm text-slate-500">
        Loading per-algorithm breakdown…
      </div>
    );
  }
  if (rows.length === 0) {
    return (
      <div className="h-64 rounded-lg border border-dashed border-slate-800 bg-slate-900/40 p-3 flex items-center justify-center text-sm text-slate-500">
        No traffic for this key yet.
      </div>
    );
  }

  // Each row is already wide: one algorithm per row with allowed/rejected
  // fields. Recharts treats each row as a group on the X axis and draws one
  // Bar series per dataKey.
  const data = rows.map((r) => ({
    algorithm: r.algorithm,
    allowed: r.allowed,
    rejected: r.rejected,
  }));

  return (
    <div className="h-64 rounded-lg border border-slate-800 bg-slate-900/60 p-3">
      <div className="px-1 pb-2 text-xs uppercase tracking-wide text-slate-500">
        Per-algorithm breakdown
      </div>
      <ResponsiveContainer width="100%" height="85%">
        <BarChart data={data} margin={{ top: 5, right: 20, bottom: 0, left: 0 }}>
          <CartesianGrid stroke="#1e293b" strokeDasharray="3 3" />
          <XAxis dataKey="algorithm" stroke="#64748b" fontSize={12} />
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
          <Bar dataKey="allowed" fill="#34d399" />
          <Bar dataKey="rejected" fill="#fb7185" />
        </BarChart>
      </ResponsiveContainer>
    </div>
  );
}

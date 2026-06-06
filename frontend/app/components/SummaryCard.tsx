"use client";

import type { SummaryResp } from "../lib/api";
import { formatNumber, formatPct } from "../lib/format";

type Props = { summary: SummaryResp | null };

export default function SummaryCard({ summary }: Props) {
  const tiles = [
    { label: "Total", value: summary ? formatNumber(summary.total) : "—", tone: "text-slate-100" },
    { label: "Allowed", value: summary ? formatNumber(summary.allowed) : "—", tone: "text-emerald-400" },
    { label: "Rejected", value: summary ? formatNumber(summary.rejected) : "—", tone: "text-rose-400" },
    {
      label: "Rejection rate",
      value: summary ? formatPct(summary.rejection_rate) : "—",
      tone: "text-amber-300",
    },
  ];

  return (
    <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
      {tiles.map((t) => (
        <div
          key={t.label}
          className="rounded-lg border border-slate-800 bg-slate-900/60 p-4"
        >
          <div className="text-xs uppercase tracking-wide text-slate-500">{t.label}</div>
          <div className={`mt-1 text-2xl font-semibold tabular-nums ${t.tone}`}>{t.value}</div>
        </div>
      ))}
    </div>
  );
}

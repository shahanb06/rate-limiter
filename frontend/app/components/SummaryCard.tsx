"use client";

import { Line, LineChart, ResponsiveContainer } from "recharts";
import type { SummaryResp, TimeseriesPoint } from "../lib/api";
import { formatNumber } from "../lib/format";

type Props = {
  summary: SummaryResp | null;
  points: TimeseriesPoint[];
};

type Tile = {
  label: string;
  value: string;
  valueClass: string;
  series: { v: number }[];
  strokeColor: string;
};

export default function SummaryCard({ summary, points }: Props) {
  const totalSeries = points.map((p) => ({ v: p.total }));
  const allowedSeries = points.map((p) => ({ v: p.allowed }));
  const rejectedSeries = points.map((p) => ({ v: p.rejected }));

  const tiles: Tile[] = [
    {
      label: "Total Requests",
      value: summary ? formatNumber(summary.total) : "—",
      valueClass: "text-slate-100",
      series: totalSeries,
      strokeColor: "#60a5fa",
    },
    {
      label: "Allowed",
      value: summary ? formatNumber(summary.allowed) : "—",
      valueClass: "text-emerald-400",
      series: allowedSeries,
      strokeColor: "#22c55e",
    },
    {
      label: "Rejected",
      value: summary ? formatNumber(summary.rejected) : "—",
      valueClass: "text-rose-400",
      series: rejectedSeries,
      strokeColor: "#f43f5e",
    },
  ];

  return (
    <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
      {tiles.map((t) => (
        <div
          key={t.label}
          className="rounded-lg border border-slate-800 bg-slate-900/60 p-4"
        >
          <div className="text-xs uppercase tracking-wide text-slate-500">
            {t.label}
          </div>
          <div
            className={`mt-1 text-2xl font-semibold tabular-nums ${t.valueClass}`}
          >
            {t.value}
          </div>
          <div className="mt-2 h-8">
            {t.series.length >= 2 && (
              <ResponsiveContainer width="100%" height="100%">
                <LineChart data={t.series}>
                  <Line
                    type="monotone"
                    dataKey="v"
                    stroke={t.strokeColor}
                    strokeWidth={1.5}
                    dot={false}
                    isAnimationActive={false}
                  />
                </LineChart>
              </ResponsiveContainer>
            )}
          </div>
        </div>
      ))}
    </div>
  );
}

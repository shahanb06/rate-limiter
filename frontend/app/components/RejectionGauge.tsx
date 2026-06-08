"use client";

import {
  PolarAngleAxis,
  RadialBar,
  RadialBarChart,
  ResponsiveContainer,
} from "recharts";
import type { SummaryResp } from "../lib/api";

type Props = { summary: SummaryResp | null };

type Band = {
  threshold: number;
  color: string;
  label: string;
};

const BANDS: Band[] = [
  { threshold: 0.3, color: "#22c55e", label: "Healthy" },
  { threshold: 0.7, color: "#f59e0b", label: "Elevated" },
  { threshold: 1.01, color: "#f43f5e", label: "Critical" },
];

function bandFor(rate: number): Band {
  return BANDS.find((b) => rate < b.threshold) ?? BANDS[BANDS.length - 1];
}

export default function RejectionGauge({ summary }: Props) {
  const rate = summary?.rejection_rate ?? 0;
  const pct = rate * 100;
  const band = bandFor(rate);

  // RadialBarChart wants data as a single-item array with the value
  // scaled to a 100-max domain. PolarAngleAxis with domain [0, 100] and
  // startAngle 180, endAngle 0 produces a clean left-to-right half donut.
  const data = [{ name: "rejection", value: pct, fill: band.color }];

  return (
    <div className="rounded-lg border border-slate-800 bg-slate-900/60 p-4">
      <div className="text-xs uppercase tracking-wide text-slate-500">
        Rejection Rate
      </div>
      <div className="relative h-40 mt-2">
        <ResponsiveContainer width="100%" height="100%">
          <RadialBarChart
            data={data}
            startAngle={180}
            endAngle={0}
            innerRadius="70%"
            outerRadius="100%"
            barSize={16}
          >
            <PolarAngleAxis
              type="number"
              domain={[0, 100]}
              angleAxisId={0}
              tick={false}
            />
            <RadialBar
              background={{ fill: "#1e293b" }}
              dataKey="value"
              cornerRadius={8}
              isAnimationActive={false}
            />
          </RadialBarChart>
        </ResponsiveContainer>
        <div className="absolute inset-x-0 bottom-2 flex flex-col items-center pointer-events-none">
          <div
            className="text-3xl font-semibold tabular-nums"
            style={{ color: band.color }}
          >
            {summary ? `${pct.toFixed(1)}%` : "—"}
          </div>
          <div className="text-xs uppercase tracking-wide text-slate-500 mt-1">
            {summary ? band.label : ""}
          </div>
        </div>
      </div>
    </div>
  );
}

"use client";

import type { LeaderboardRow } from "../lib/api";
import { formatNumber, formatPct } from "../lib/format";

type Props = {
  rows: LeaderboardRow[] | null;
  selected: string | null;
  onSelect: (key: string) => void;
};

export default function LeaderboardTable({ rows, selected, onSelect }: Props) {
  if (rows === null) {
    return (
      <div className="rounded-lg border border-slate-800 bg-slate-900/60 p-6 text-center text-sm text-slate-500">
        Loading leaderboard…
      </div>
    );
  }
  if (rows.length === 0) {
    return (
      <div className="rounded-lg border border-dashed border-slate-800 bg-slate-900/40 p-6 text-center text-sm text-slate-500">
        No keys yet.
      </div>
    );
  }

  return (
    <div className="overflow-hidden rounded-lg border border-slate-800 bg-slate-900/60">
      <div className="border-b border-slate-800 px-4 py-2 text-xs uppercase tracking-wide text-slate-500">
        Keys leaderboard · ordered by volume · click a row to inspect
      </div>
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b border-slate-800 text-left text-xs uppercase tracking-wide text-slate-500">
            <th className="px-4 py-2 font-medium">Key</th>
            <th className="px-4 py-2 font-medium text-right">Total</th>
            <th className="px-4 py-2 font-medium text-right">Allowed</th>
            <th className="px-4 py-2 font-medium text-right">Rejected</th>
            <th className="px-4 py-2 font-medium text-right">Rejection rate</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => {
            const isSelected = row.key === selected;
            return (
              <tr
                key={row.key}
                onClick={() => onSelect(row.key)}
                className={
                  "cursor-pointer border-b border-slate-800/60 last:border-b-0 transition-colors " +
                  (isSelected
                    ? "bg-sky-950/60 text-sky-100"
                    : "hover:bg-slate-800/40")
                }
              >
                <td className="px-4 py-2 font-mono">{row.key}</td>
                <td className="px-4 py-2 text-right tabular-nums">
                  {formatNumber(row.total)}
                </td>
                <td className="px-4 py-2 text-right tabular-nums text-emerald-400">
                  {formatNumber(row.allowed)}
                </td>
                <td className="px-4 py-2 text-right tabular-nums text-rose-400">
                  {formatNumber(row.rejected)}
                </td>
                <td className="px-4 py-2 text-right tabular-nums text-amber-300">
                  {formatPct(row.rejection_rate)}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

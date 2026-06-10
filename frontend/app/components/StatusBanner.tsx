"use client";

type Tone = "info" | "warn" | "error";

const tones: Record<Tone, string> = {
  info: "border-slate-700 bg-[var(--surface)] text-slate-300",
  warn: "border-amber-800 bg-amber-950/40 text-amber-200",
  error: "border-rose-800 bg-rose-950/40 text-rose-200",
};

export default function StatusBanner({
  tone = "info",
  children,
}: {
  tone?: Tone;
  children: React.ReactNode;
}) {
  return (
    <div className={`rounded-lg border px-4 py-3 text-sm ${tones[tone]}`}>
      {children}
    </div>
  );
}

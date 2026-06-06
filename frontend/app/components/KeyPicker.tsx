"use client";

type Props = {
  keys: string[];
  selected: string | null;
  onSelect: (key: string) => void;
  disabled?: boolean;
};

export default function KeyPicker({ keys, selected, onSelect, disabled }: Props) {
  return (
    <div className="flex items-center gap-3">
      <label htmlFor="key-picker" className="text-sm text-slate-400">
        Key
      </label>
      <select
        id="key-picker"
        className="bg-slate-800 border border-slate-700 rounded-md px-3 py-1.5 text-sm focus:outline-none focus:border-sky-500 disabled:opacity-50"
        value={selected ?? ""}
        disabled={disabled || keys.length === 0}
        onChange={(e) => onSelect(e.target.value)}
      >
        {selected === null && <option value="">Select a key…</option>}
        {keys.map((k) => (
          <option key={k} value={k}>
            {k}
          </option>
        ))}
      </select>
    </div>
  );
}

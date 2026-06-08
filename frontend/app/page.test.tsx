import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

vi.mock("./lib/api", () => ({
  API_BASE_URL: "http://test.local",
  getKeys: vi.fn(),
  getSummary: vi.fn(),
  getTimeseries: vi.fn(),
  getSummaryByAlgorithm: vi.fn(),
  getLeaderboard: vi.fn(),
}));

// Mock chart components — these tests are about logic (state/effects), not
// recharts SVG output. Stubs render the input length as text so we can still
// observe that data reached the panel if we want to.
vi.mock("./components/TimeseriesChart", () => ({
  default: ({ points }: { points: unknown[] }) => (
    <div data-testid="ts-chart">ts:{points.length}</div>
  ),
}));
vi.mock("./components/RejectionRateChart", () => ({
  default: ({ points }: { points: unknown[] }) => (
    <div data-testid="rej-chart">rej:{points.length}</div>
  ),
}));
vi.mock("./components/RejectionGauge", () => ({
  default: ({ summary }: { summary: unknown }) => (
    <div data-testid="rej-gauge">gauge:{summary ? "ok" : "null"}</div>
  ),
}));

import * as api from "./lib/api";
import Dashboard from "./page";

const okSummary = (key: string, total: number) => ({
  ok: true as const,
  data: { key, allowed: 0, rejected: 0, total, rejection_rate: 0 },
});
const okTimeseries = (key: string) => ({
  ok: true as const,
  data: { key, since: "1h", points: [] },
});
const okAlg = (key: string) => ({
  ok: true as const,
  data: { key, by_algorithm: [] },
});
const okLeaderboard = { ok: true as const, data: { rows: [] } };

beforeEach(() => {
  vi.mocked(api.getKeys).mockReset();
  vi.mocked(api.getSummary).mockReset();
  vi.mocked(api.getTimeseries).mockReset();
  vi.mocked(api.getSummaryByAlgorithm).mockReset();
  vi.mocked(api.getLeaderboard).mockReset();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("Dashboard", () => {
  test("stale-key race: A's response after switch to B does NOT overwrite B's panel", async () => {
    let resolveA!: (v: unknown) => void;
    let resolveB!: (v: unknown) => void;

    vi.mocked(api.getKeys).mockResolvedValue({
      ok: true,
      data: { keys: ["A", "B"] },
    });
    // Other fetches must resolve — otherwise Promise.all never settles and the
    // tick body that processes the summary never runs.
    vi.mocked(api.getLeaderboard).mockResolvedValue(okLeaderboard);
    vi.mocked(api.getTimeseries).mockImplementation((key: string) =>
      Promise.resolve(okTimeseries(key)),
    );
    vi.mocked(api.getSummaryByAlgorithm).mockImplementation((key: string) =>
      Promise.resolve(okAlg(key)),
    );
    vi.mocked(api.getSummary).mockImplementation((key: string) => {
      return new Promise((resolve) => {
        if (key === "A") resolveA = resolve;
        else if (key === "B") resolveB = resolve;
      }) as ReturnType<typeof api.getSummary>;
    });

    render(<Dashboard />);

    // Key list loads → A selected → first per-key tick captures resolveA.
    await waitFor(() => expect(resolveA).toBeDefined());

    // Switch to B before A's summary resolves.
    fireEvent.change(screen.getByLabelText("Key"), { target: { value: "B" } });
    await waitFor(() => expect(resolveB).toBeDefined());

    // Resolve A AFTER the switch. The cancelled flag on the old effect must
    // drop this write — sentinel value 999111 should never appear in the DOM.
    await act(async () => {
      resolveA(okSummary("A", 999111));
    });

    // Resolve B — its value should be the one rendered.
    await act(async () => {
      resolveB(okSummary("B", 42));
    });

    expect(screen.queryByText("999,111")).not.toBeInTheDocument();
    expect(screen.getByText("42")).toBeInTheDocument();
  });

  test("interval cleanup: every setInterval is paired with a clearInterval; unmount stops further fetches", async () => {
    vi.mocked(api.getKeys).mockResolvedValue({
      ok: true,
      data: { keys: ["A", "B"] },
    });
    vi.mocked(api.getLeaderboard).mockResolvedValue(okLeaderboard);
    vi.mocked(api.getSummary).mockImplementation((key: string) =>
      Promise.resolve(okSummary(key, 0)),
    );
    vi.mocked(api.getTimeseries).mockImplementation((key: string) =>
      Promise.resolve(okTimeseries(key)),
    );
    vi.mocked(api.getSummaryByAlgorithm).mockImplementation((key: string) =>
      Promise.resolve(okAlg(key)),
    );

    // Spy on global setInterval/clearInterval, but isolate the APP's intervals
    // from testing-library's own polling (waitFor uses setInterval internally).
    // The app polls every POLL_MS = 7000ms; @testing-library/dom uses a much
    // smaller default (~50ms). Filtering by the delay argument cleanly
    // separates the two sources. App interval IDs are captured from
    // mock.results.value; clearInterval matches against that captured set so
    // testing-library's clears don't pollute the count either.
    const APP_POLL_MS = 7000;
    const setSpy = vi.spyOn(window, "setInterval");
    const clearSpy = vi.spyOn(window, "clearInterval");

    const appCreatedIds = (): number[] =>
      setSpy.mock.calls
        .map((args, i) => ({ delay: args[1], id: setSpy.mock.results[i].value as number }))
        .filter((c) => c.delay === APP_POLL_MS)
        .map((c) => c.id);

    const appClearedIds = (): number[] => {
      const created = new Set(appCreatedIds());
      return clearSpy.mock.calls
        .map((args) => args[0] as number)
        .filter((id) => created.has(id));
    };

    const activeAppIntervals = (): number => appCreatedIds().length - appClearedIds().length;

    const { unmount } = render(<Dashboard />);

    // After mount + getKeys resolved + selected="A": the app has created two
    // intervals (one for selected=null, one for selected="A") and cleared the
    // first. Exactly one app-owned interval is active.
    await waitFor(() => expect(screen.getByDisplayValue("A")).toBeInTheDocument());
    await waitFor(() => expect(activeAppIntervals()).toBe(1));

    const idBeforeSwitch = appCreatedIds().at(-1)!;
    const createdBefore = appCreatedIds().length;
    const clearedBefore = appClearedIds().length;

    // Switch keys. The "no double-poll" guarantee: the old interval must be
    // cleared (its id appears in appClearedIds) AND a brand-new interval
    // started, with active count remaining exactly 1 throughout — never 2.
    fireEvent.change(screen.getByLabelText("Key"), { target: { value: "B" } });
    await waitFor(() => {
      expect(appCreatedIds().length).toBe(createdBefore + 1);
      expect(appClearedIds().length).toBe(clearedBefore + 1);
    });
    expect(appClearedIds()).toContain(idBeforeSwitch); // old interval was cleared
    expect(activeAppIntervals()).toBe(1); // no leftover second interval

    // Unmount → the active app interval is cleared; no further fetches.
    const fetchesBeforeUnmount = vi.mocked(api.getSummary).mock.calls.length;
    unmount();
    expect(activeAppIntervals()).toBe(0);

    await new Promise((r) => setTimeout(r, 30));
    expect(vi.mocked(api.getSummary).mock.calls.length).toBe(fetchesBeforeUnmount);
  });

  test("per-fetch error: leaderboard fails but summary succeeds — error banner + summary both visible", async () => {
    vi.mocked(api.getKeys).mockResolvedValue({
      ok: true,
      data: { keys: ["A"] },
    });
    vi.mocked(api.getLeaderboard).mockResolvedValue({ ok: false, error: "boom-leaderboard" });
    vi.mocked(api.getSummary).mockResolvedValue({
      ok: true,
      data: { key: "A", allowed: 7, rejected: 3, total: 10, rejection_rate: 0.3 },
    });
    vi.mocked(api.getTimeseries).mockResolvedValue(okTimeseries("A"));
    vi.mocked(api.getSummaryByAlgorithm).mockResolvedValue(okAlg("A"));

    render(<Dashboard />);

    await waitFor(() => expect(screen.getByText("10")).toBeInTheDocument());
    // SummaryCard panels rendered with summary data.
    expect(screen.getByText("7")).toBeInTheDocument();
    expect(screen.getByText("3")).toBeInTheDocument();
    // Error banner surfaces leaderboard's error.
    expect(screen.getByText(/boom-leaderboard/)).toBeInTheDocument();
  });

  test("key switch resets per-key panels so previous key's summary does not bleed", async () => {
    let resolveB!: (v: unknown) => void;

    vi.mocked(api.getKeys).mockResolvedValue({
      ok: true,
      data: { keys: ["A", "B"] },
    });
    vi.mocked(api.getLeaderboard).mockResolvedValue(okLeaderboard);
    vi.mocked(api.getTimeseries).mockImplementation((key: string) =>
      Promise.resolve(okTimeseries(key)),
    );
    vi.mocked(api.getSummaryByAlgorithm).mockImplementation((key: string) =>
      Promise.resolve(okAlg(key)),
    );
    vi.mocked(api.getSummary).mockImplementation((key: string) => {
      if (key === "A") return Promise.resolve(okSummary("A", 12345));
      return new Promise((resolve) => {
        resolveB = resolve;
      }) as ReturnType<typeof api.getSummary>;
    });

    render(<Dashboard />);
    await waitFor(() => expect(screen.getByText("12,345")).toBeInTheDocument());

    // Switch to B; B's summary is still pending so panel should clear A's number.
    fireEvent.change(screen.getByLabelText("Key"), { target: { value: "B" } });
    await waitFor(() => expect(screen.queryByText("12,345")).not.toBeInTheDocument());

    // Resolve B; it should refetch and render cleanly with B's value.
    await act(async () => {
      resolveB(okSummary("B", 55555));
    });
    expect(screen.getByText("55,555")).toBeInTheDocument();

    // And api.getSummary was called for both keys.
    const callsByKey = vi.mocked(api.getSummary).mock.calls.map((c) => c[0]);
    expect(callsByKey).toContain("A");
    expect(callsByKey).toContain("B");
  });
});

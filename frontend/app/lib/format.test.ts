import { describe, expect, test } from "vitest";
import { rejectionRateSeries } from "./format";

describe("rejectionRateSeries", () => {
  test("empty input → empty output", () => {
    expect(rejectionRateSeries([])).toEqual([]);
  });

  test("zero-total buckets produce rate=0, never NaN", () => {
    const out = rejectionRateSeries([
      { bucket_start: "2026-06-07T12:00:00Z", rejected: 0, total: 0 },
      { bucket_start: "2026-06-07T12:00:15Z", rejected: 5, total: 0 },
    ]);
    expect(out).toHaveLength(2);
    expect(out[0].rate).toBe(0);
    expect(out[1].rate).toBe(0);
    expect(Number.isNaN(out[0].rate)).toBe(false);
    expect(Number.isNaN(out[1].rate)).toBe(false);
  });

  test("normal points → rejected/total", () => {
    const out = rejectionRateSeries([
      { bucket_start: "2026-06-07T12:00:00Z", rejected: 2, total: 10 },
      { bucket_start: "2026-06-07T12:00:15Z", rejected: 7, total: 7 },
      { bucket_start: "2026-06-07T12:00:30Z", rejected: 0, total: 100 },
    ]);
    expect(out[0].rate).toBeCloseTo(0.2);
    expect(out[1].rate).toBe(1);
    expect(out[2].rate).toBe(0);
  });
});

import { describe, it, expect } from "vitest";
import { TIME_RANGES, sinceForRange } from "@/lib/activity-range";

// activity-range — pure helpers backing the /api-keys/activity time-range chips
// (FUT-088 #1). These convert a window label into a real RFC3339 `since` lower
// bound, replacing the old limit-as-time-proxy hack.

describe("sinceForRange", () => {
  // A fixed reference instant so the expected offsets are deterministic.
  const now = Date.UTC(2026, 6, 15, 12, 0, 0); // 2026-07-15T12:00:00Z

  it("returns now minus 24 hours for the 24h window", () => {
    const got = sinceForRange("24h", now);
    expect(got).toBe(new Date(now - 24 * 3600_000).toISOString());
  });

  it("returns now minus 7 days for the 7d window", () => {
    const got = sinceForRange("7d", now);
    expect(got).toBe(new Date(now - 7 * 24 * 3600_000).toISOString());
  });

  it("returns now minus 30 days for the 30d window", () => {
    const got = sinceForRange("30d", now);
    expect(got).toBe(new Date(now - 30 * 24 * 3600_000).toISOString());
  });

  it("produces a parseable RFC3339 timestamp strictly before now", () => {
    const got = sinceForRange("7d", now);
    expect(Date.parse(got)).toBeLessThan(now);
    expect(Number.isNaN(Date.parse(got))).toBe(false);
  });
});

describe("TIME_RANGES", () => {
  it("exposes the 24h/7d/30d windows in ascending order", () => {
    expect(TIME_RANGES.map((r) => r.label)).toEqual(["24h", "7d", "30d"]);
  });

  it("keeps every window's page limit within the backend cap of 200", () => {
    for (const r of TIME_RANGES) {
      expect(r.limit).toBeLessThanOrEqual(200);
      expect(r.limit).toBeGreaterThan(0);
    }
  });
});

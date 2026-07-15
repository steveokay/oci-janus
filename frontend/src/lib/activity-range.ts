// activity-range — pure helpers backing the /api-keys/activity time-range chips
// (FUT-088 #1).
//
// The chips previously faked a 24h/7d/30d window by asking the backend for a
// larger `limit` (50 / 200 / 500) and hoping events were evenly distributed —
// a hack that both under- and over-counted, and whose 30d value (500) exceeded
// the backend's hard cap of 200 and returned a 400. Now each window maps to a
// real RFC3339 `since` lower bound that the auth endpoint threads into the
// audit query (GetNotificationsRequest.since), so the feed is genuinely
// time-bounded server-side. `limit` reverts to being a page size, capped at the
// backend's 200 ceiling.

// TimeRange is one selectable window: a display label, the lookback duration in
// hours used to compute `since`, and the per-page event limit (≤ 200 backend cap).
export interface TimeRange {
  label: TimeRangeLabel;
  hours: number;
  limit: number;
}

// TimeRangeLabel is the closed set of window labels rendered as chips.
export type TimeRangeLabel = "24h" | "7d" | "30d";

// TIME_RANGES lists the selectable windows in ascending duration order. `limit`
// is now just the initial page size (well within the backend's 200-row cap) —
// time bounding via `since`, not `limit`, is what scopes the window. "Load more"
// raises the page size toward the cap; beyond that the operator narrows the
// window, which now genuinely shrinks the result set.
export const TIME_RANGES: readonly TimeRange[] = [
  { label: "24h", hours: 24, limit: 100 },
  { label: "7d", hours: 24 * 7, limit: 100 },
  { label: "30d", hours: 24 * 30, limit: 100 },
] as const;

// PAGE_LIMIT_CAP mirrors the auth endpoint's hard ceiling on `limit`. "Load
// more" clamps to this so a limit-based expansion never trips the backend's
// 400 (the bug the old 30d window's limit=500 hit).
export const PAGE_LIMIT_CAP = 200;

// Default window applied on first render and used as the fallback when an
// unknown label is passed. 7d matches the audit service's own default window.
export const DEFAULT_RANGE: TimeRangeLabel = "7d";

// sinceForRange converts a window label into an RFC3339 `since` timestamp:
// `nowMs` minus the window's lookback duration. `nowMs` is passed in (rather
// than read from Date.now() inside) so callers can memoize a stable value —
// recomputing it every render would churn the value and defeat query caching —
// and so the helper is deterministic under test.
export function sinceForRange(label: TimeRangeLabel, nowMs: number): string {
  const hours =
    TIME_RANGES.find((r) => r.label === label)?.hours ?? 24 * 7;
  return new Date(nowMs - hours * 3600_000).toISOString();
}

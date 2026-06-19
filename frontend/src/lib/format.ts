import { formatDistanceToNowStrict, format as dfFormat, parseISO } from "date-fns";

// Beacon — formatting primitives. Keep all the unit / locale work in one
// place so every screen renders bytes + dates the same way.

const BYTE_UNITS = ["B", "KB", "MB", "GB", "TB", "PB"] as const;

// `formatBytes(0)` → "0 B"; `formatBytes(1536)` → "1.5 KB".
// Decimals default to 1 for KB/MB/GB and 2 for TB/PB so the number reads tight.
export function formatBytes(bytes: number, decimals?: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B";
  const k = 1024;
  const i = Math.min(
    BYTE_UNITS.length - 1,
    Math.floor(Math.log(bytes) / Math.log(k)),
  );
  const value = bytes / Math.pow(k, i);
  const places = decimals ?? (i >= 4 ? 2 : i >= 1 ? 1 : 0);
  return `${value.toFixed(places)} ${BYTE_UNITS[i]}`;
}

// Compact form for hero KPI numbers: "1.2K", "3.4M".
const COMPACT = new Intl.NumberFormat(undefined, {
  notation: "compact",
  maximumFractionDigits: 1,
});
export function formatCompactNumber(n: number): string {
  if (!Number.isFinite(n)) return "0";
  return COMPACT.format(n);
}

// "5 minutes ago", "3 days ago" — `strict` keeps the suffix consistent.
export function formatRelativeDate(iso: string | undefined | null): string {
  if (!iso) return "—";
  try {
    return formatDistanceToNowStrict(parseISO(iso), { addSuffix: true });
  } catch {
    return "—";
  }
}

// "Jun 19, 2026 14:23" — used in detail surfaces where the exact time matters.
export function formatAbsoluteDate(iso: string | undefined | null): string {
  if (!iso) return "—";
  try {
    return dfFormat(parseISO(iso), "MMM d, yyyy HH:mm");
  } catch {
    return "—";
  }
}

// Build the `docker pull` command we show on repository detail pages.
// The hostname will come from FE-API-007 when it lands; until then we
// surface the dev gateway as a sensible default.
export function pullCommand(
  org: string,
  repo: string,
  tag = "latest",
  host = "registry.localhost",
): string {
  return `docker pull ${host}/${org}/${repo}:${tag}`;
}

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

// F4 follow-up — emit the right CLI for the artifact type a repository
// holds. `image` → `docker pull host/org/repo:tag` (existing behaviour).
// `helm` → `helm pull oci://host/org/repo --version <tag>` (charts use
// the OCI scheme + --version flag; the tag never appears after a colon).
// Unknown / empty type falls back to docker pull so legacy repos and
// the no-tags-yet first paint stay sensible.
//
// Returned shape carries the label + verb separately so the card can
// re-headline "Pull this image" → "Pull this chart" without rebuilding
// the command string elsewhere.
export interface PullCommandSpec {
  // The shell command line, including the `helm` or `docker` prefix.
  cmd: string;
  // Card heading — what the operator is being shown how to do.
  heading: string;
  // Short subject noun ("image" | "chart" | "artifact") for the body
  // copy if a caller needs to interpolate it.
  artifact: string;
}

export function pullCommandFor(
  artifactType: string | undefined,
  org: string,
  repo: string,
  tag = "latest",
  host = "registry.localhost",
): PullCommandSpec {
  switch (artifactType) {
    case "helm":
      return {
        cmd: `helm pull oci://${host}/${org}/${repo} --version ${tag}`,
        heading: "Pull this chart",
        artifact: "chart",
      };
    default:
      return {
        cmd: `docker pull ${host}/${org}/${repo}:${tag}`,
        heading: "Pull this image",
        artifact: "image",
      };
  }
}

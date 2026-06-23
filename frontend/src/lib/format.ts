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

// Compact "5m / 3h / 2d ago" form for cramped layouts (dropdown rows, table
// cells). Mirrors formatRelativeDate but trades the long suffix for short
// unit letters. Use formatRelativeDate when the surface has room for prose.
export function formatShortRelativeDate(iso: string | undefined | null): string {
  if (!iso) return "—";
  try {
    const then = parseISO(iso).getTime();
    const diffSec = Math.floor((Date.now() - then) / 1000);
    if (!Number.isFinite(diffSec) || diffSec < 0) return "—";
    if (diffSec < 60) return "just now";
    if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m ago`;
    if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h ago`;
    return `${Math.floor(diffSec / 86400)}d ago`;
  } catch {
    return "—";
  }
}

// shortenKey collapses a long key_id / SHA256 string to "aaaaaaaa…bbbb"
// so it fits one line in tight layouts (dropdown rows, alert prompts).
// Returns the input unchanged when it's already short enough to fit.
export function shortenKey(k: string): string {
  if (!k || k.length <= 20) return k ?? "";
  return `${k.slice(0, 8)}…${k.slice(-4)}`;
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
// A single numbered step in the pull/install walkthrough — a short
// label ("Pull the chart") above the actual shell command. The card
// renders each step as its own monospaced snippet with its own copy
// button so an operator can grab just the command they need.
export interface PullCommandStep {
  label: string;
  cmd: string;
}

export interface PullCommandSpec {
  // First-step shell command, kept as a backwards-compat shorthand
  // for older callers (TagHeader's inline copy snippet). Equivalent
  // to `steps.find(s => s.label.startsWith('Pull'))?.cmd` for the new
  // multi-step shape.
  cmd: string;
  // Card heading — what the operator is being shown how to do.
  heading: string;
  // Short subject noun ("image" | "chart" | "artifact") for the body
  // copy if a caller needs to interpolate it.
  artifact: string;
  // Numbered walkthrough rendered by PullCommandCard. The first step
  // is always the login (one-time, copy-once), then the primary
  // verb (pull), then optional follow-ups (helm install).
  steps: PullCommandStep[];
}

// looksLocalHost is a heuristic: true when the workspace host looks
// like the local dev stack (loopback or explicit dev domain). We use
// it to decide whether to append `--plain-http` to helm commands and
// `--insecure` hints to docker login, since the dev gateway serves
// HTTP only. Production / custom-domain hosts get clean HTTPS-ready
// commands without the flag. Hostnames that include a port (`:8081`)
// also count as local because production hosts on standard 443
// rarely carry one.
function looksLocalHost(host: string): boolean {
  const h = host.toLowerCase();
  if (h.includes("localhost") || h.includes("127.0.0.1") || h.includes(".local")) {
    return true;
  }
  // host:port → port presence implies dev / non-443 endpoint
  return /:\d+($|\/)/.test(h);
}

export function pullCommandFor(
  artifactType: string | undefined,
  org: string,
  repo: string,
  tag = "latest",
  host = "registry.localhost",
): PullCommandSpec {
  const plain = looksLocalHost(host) ? " --plain-http" : "";
  const insecureNote = looksLocalHost(host) ? " # dev stack: HTTP" : "";
  const ref = `oci://${host}/${org}/${repo}`;

  switch (artifactType) {
    case "helm": {
      // Helm chart walkthrough. `helm registry login` is a once-per-host
      // step; `helm pull` is what the existing card surfaced; `helm
      // install` is the natural follow-up the operator usually wants
      // anyway. `--plain-http` only appears on local-looking hosts —
      // production charts served behind real TLS get clean commands.
      return {
        cmd: `helm pull ${ref} --version ${tag}${plain}`,
        heading: "Pull this chart",
        artifact: "chart",
        steps: [
          {
            label: "Login (one-time)",
            cmd: `helm registry login ${host} -u <user>${plain}`,
          },
          {
            label: "Pull the chart",
            cmd: `helm pull ${ref} --version ${tag}${plain}`,
          },
          {
            label: "Or install directly",
            cmd: `helm install my-release ${ref} --version ${tag}${plain}`,
          },
        ],
      };
    }
    default: {
      // Container image walkthrough. Docker doesn't have a `--plain-http`
      // flag — operators have to add the host to `insecure-registries`
      // in dockerd config when they're hitting a local HTTP gateway, so
      // we show that as a side comment instead of inlining a flag.
      return {
        cmd: `docker pull ${host}/${org}/${repo}:${tag}`,
        heading: "Pull this image",
        artifact: "image",
        steps: [
          {
            label: "Login (one-time)",
            cmd: `docker login ${host} -u <user>${insecureNote}`,
          },
          {
            label: "Pull the image",
            cmd: `docker pull ${host}/${org}/${repo}:${tag}`,
          },
          {
            label: "Or run directly",
            cmd: `docker run --rm ${host}/${org}/${repo}:${tag}`,
          },
        ],
      };
    }
  }
}

import * as React from "react";
import { cn } from "@/lib/utils";

// S-MAINT-1 P5 — shared page-size selector used by the four "operational"
// tables (Vulnerabilities, Scans, Remediation, Notifications).
//
// The component renders a tiny "Show: 20 / 50 / 100" cluster sitting just
// above each table so an operator can balance "scroll vs. fetch" without
// digging into URL params. Selection persists in localStorage keyed by
// `storageKey` (one key per surface) so the last choice survives a
// browser refresh and re-mounts.
//
// Two pieces:
//   • usePageSize — the hook with localStorage round-trip + safe defaults
//   • PageSizeSelector — the visual chip cluster (display only; pass the
//     hook's state in)

export const DEFAULT_PAGE_SIZE = 20;
export const PAGE_SIZE_CHOICES = [20, 50, 100] as const;
export type PageSize = (typeof PAGE_SIZE_CHOICES)[number];

const STORAGE_PREFIX = "beacon.pageSize.";

// usePageSize — read/write a per-surface page-size choice in localStorage.
//
// `storageKey` is a short identifier per table ("vulns", "scans", etc.).
// The hook gracefully degrades when localStorage is unavailable (SSR /
// privacy mode) — it returns the default and treats setPageSize as a no-op
// on the persistence side, in-memory state still updates.
export function usePageSize(
  storageKey: string,
  defaultSize: PageSize = DEFAULT_PAGE_SIZE,
): [PageSize, (next: PageSize) => void] {
  const fullKey = STORAGE_PREFIX + storageKey;

  // Read the initial value synchronously so the very first fetch already
  // uses the persisted size (avoids a useEffect re-fetch flicker).
  const [size, setSize] = React.useState<PageSize>(() => {
    if (typeof window === "undefined") return defaultSize;
    try {
      const raw = window.localStorage.getItem(fullKey);
      if (!raw) return defaultSize;
      const parsed = Number.parseInt(raw, 10);
      // Only honour values that are still in our allowlist — if a future
      // version of the app removes a size, stale localStorage entries
      // fall back to the default cleanly.
      if (PAGE_SIZE_CHOICES.includes(parsed as PageSize)) {
        return parsed as PageSize;
      }
      return defaultSize;
    } catch {
      return defaultSize;
    }
  });

  const update = React.useCallback(
    (next: PageSize) => {
      setSize(next);
      if (typeof window === "undefined") return;
      try {
        window.localStorage.setItem(fullKey, String(next));
      } catch {
        // Privacy mode / quota exceeded — silently ignore. In-memory
        // state still updates so the user sees the change this session.
      }
    },
    [fullKey],
  );

  return [size, update];
}

interface PageSizeSelectorProps {
  value: PageSize;
  onChange: (next: PageSize) => void;
  // `label` overrides the default "Show" prefix — used when a surface wants
  // wording like "Per page" to match its visual register.
  label?: string;
  className?: string;
}

// PageSizeSelector — visual cluster of three pill buttons. Active button
// has the accent treatment; non-active uses the muted text style so the
// cluster stays in the row header's visual register.
export function PageSizeSelector({
  value,
  onChange,
  label = "Show",
  className,
}: PageSizeSelectorProps): React.ReactElement {
  return (
    <div
      className={cn(
        "flex items-center gap-1.5 text-[11px] text-[var(--color-fg-muted)]",
        className,
      )}
      // role=group + label so the cluster is announced as a single control
      // by screen readers rather than three orphan buttons.
      role="group"
      aria-label="Page size"
    >
      <span className="uppercase tracking-[0.14em] text-[var(--color-fg-subtle)]">
        {label}
      </span>
      <div className="flex gap-0.5 rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] p-0.5">
        {PAGE_SIZE_CHOICES.map((n) => {
          const active = n === value;
          return (
            <button
              key={n}
              type="button"
              onClick={() => onChange(n)}
              aria-pressed={active}
              className={cn(
                "rounded-sm px-2 py-0.5 font-mono text-[11px] transition-colors",
                active
                  ? "bg-[var(--color-surface-sunken)] text-[var(--color-fg)]"
                  : "text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]",
              )}
            >
              {n}
            </button>
          );
        })}
      </div>
    </div>
  );
}

import * as React from "react";
import { useQuery } from "@tanstack/react-query";
import { Activity, ExternalLink } from "lucide-react";
import { apiClientRaw } from "@/lib/api/client";
import { cn } from "@/lib/utils";

// Beacon — page footer. Always visible at the bottom of the content area
// (sticky-below-content, not floating). Three slots:
//   left   — brand mark + tagline
//   middle — live BFF health dot (polls /healthz so the operator sees red
//            within ~30s if the management API drops). Deliberately quiet:
//            green dot is the default, no label until something breaks.
//   right  — links: changelog, docs, GitHub
//
// The footer reads as a status bar — a small piece of operational comfort.

const FOOTER_LINKS: Array<{ label: string; href: string; external?: boolean }> = [
  { label: "Docs", href: "https://docs.example.com", external: true },
  {
    label: "GitHub",
    href: "https://github.com/steveokay/oci-janus",
    external: true,
  },
];

export function Footer(): React.ReactElement {
  const { state, label, tone } = useBackendHealth();

  return (
    <footer
      className={cn(
        "flex h-9 shrink-0 items-center justify-between gap-4 border-t border-[var(--color-border)]",
        "bg-[var(--color-surface-2)] px-6 text-xs text-[var(--color-fg-muted)]",
      )}
    >
      <div className="flex items-center gap-2">
        <span
          className="grid size-4 place-items-center rounded-sm bg-[var(--color-accent)] text-[var(--color-accent-fg)]"
          aria-hidden
        >
          <span className="font-display text-[10px] font-semibold leading-none">
            J
          </span>
        </span>
        <span className="font-medium text-[var(--color-fg)]">Janus</span>
        <span className="text-[var(--color-fg-subtle)]">
          · Beacon UI v0.1
        </span>
      </div>

      {/* Health indicator — middle on wide screens, hidden on mobile to
          keep the footer single-line. */}
      <div
        className="hidden items-center gap-1.5 md:flex"
        title={`Management API: ${label}`}
      >
        <Activity
          className={cn(
            "size-3",
            tone === "success" && "text-[var(--color-success)]",
            tone === "warning" && "text-[var(--color-warning)]",
            tone === "danger" && "text-[var(--color-danger)]",
          )}
          aria-hidden
        />
        <span className="text-[10px] uppercase tracking-[0.16em]">
          BFF{" "}
          <span
            className={cn(
              tone === "success" && "text-[var(--color-success)]",
              tone === "warning" && "text-[var(--color-warning)]",
              tone === "danger" && "text-[var(--color-danger)]",
            )}
          >
            {state}
          </span>
        </span>
      </div>

      <nav className="flex items-center gap-4">
        {FOOTER_LINKS.map((l) => (
          <a
            key={l.label}
            href={l.href}
            target={l.external ? "_blank" : undefined}
            rel={l.external ? "noreferrer noopener" : undefined}
            className="inline-flex items-center gap-1 text-[var(--color-fg-muted)] transition-colors hover:text-[var(--color-fg)]"
          >
            {l.label}
            {l.external ? (
              <ExternalLink className="size-3" aria-hidden />
            ) : null}
          </a>
        ))}
      </nav>
    </footer>
  );
}

// useBackendHealth — polls the management /healthz every 30 s.
// Returns a three-state result keyed to our semantic tones.
function useBackendHealth(): {
  state: string;
  label: string;
  tone: "success" | "warning" | "danger";
} {
  const { data, isError, isFetching, fetchStatus, isLoading } = useQuery({
    queryKey: ["health", "bff"],
    queryFn: async () => {
      // Don't use apiClient here — we don't want the 401 interceptor to
      // attempt a refresh on a healthcheck. apiClientRaw skips interceptors.
      // /healthz lives at root (not /api/v1), so we override baseURL with "/".
      const res = await apiClientRaw.get("/healthz", {
        baseURL: "/",
        timeout: 4_000,
      });
      return res.status === 200;
    },
    refetchInterval: 30_000,
    retry: 0,
    staleTime: 25_000,
  });

  if (isLoading || (isFetching && fetchStatus === "fetching" && data === undefined)) {
    return { state: "checking", label: "checking…", tone: "warning" };
  }
  if (isError || data === false) {
    return { state: "down", label: "unreachable", tone: "danger" };
  }
  return { state: "healthy", label: "healthy", tone: "success" };
}

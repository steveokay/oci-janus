import * as React from "react";
import { ShieldCheck } from "lucide-react";
import { PreviewBanner } from "@/components/access/PreviewBanner";

// Dummy trust relationship data — illustrative only (FUT-001 preview).
// Real OIDC workload-identity records will ship in Sprint 11.
interface TrustRelationship {
  id: string;
  provider: string;
  subject: string;
  environment?: string;
  lastVerified: string;
}

const DUMMY_TRUSTS: TrustRelationship[] = [
  {
    id: "1",
    provider: "GitHub Actions",
    subject: "steveokay/oci-janus",
    environment: "prod",
    lastVerified: "2h ago",
  },
  {
    id: "2",
    provider: "GitLab CI",
    subject: "myorg/charts",
    lastVerified: "yesterday",
  },
  {
    id: "3",
    provider: "Buildkite",
    subject: "infra-pipelines",
    lastVerified: "never verified",
  },
];

// TrustPreview — illustrative preview of the federated workload-identity
// surface (FUT-001, shipping Sprint 11). All interactive controls are fully
// disabled and carry aria-disabled + aria-describedby so screen readers can
// explain why.
export function TrustPreview(): React.ReactElement {
  return (
    <div className="space-y-6">
      {/* Page header — lower-contrast "Preview" kicker reinforces draft state. */}
      <header className="flex flex-col gap-1">
        <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          Preview
        </p>
        <h1 className="font-display text-3xl font-medium tracking-tight">
          Federated trust
        </h1>
        <p className="text-sm text-[var(--color-fg-muted)]">
          Allow CI runners to authenticate with a short-lived registry token
          via OIDC — no static API key required.
        </p>
      </header>

      {/* Amber preview notice. */}
      <PreviewBanner sprint="Sprint 11" futureID="FUT-001" />

      {/* Hidden description for disabled controls — referenced via aria-describedby. */}
      <p id="trust-disabled-reason" className="sr-only">
        Available in Sprint 11 (FUT-001). This control is not yet functional.
      </p>

      {/* Trust relationship cards. */}
      <ul className="space-y-3" aria-label="Configured trust relationships">
        {DUMMY_TRUSTS.map((trust) => (
          <li
            key={trust.id}
            className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-surface)] p-6"
          >
            <div className="flex items-start justify-between gap-4">
              <div className="flex items-center gap-3">
                {/* Provider icon placeholder. */}
                <div
                  aria-hidden="true"
                  className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-[var(--color-bg-subtle)]"
                >
                  <ShieldCheck className="size-4 text-[var(--color-fg-muted)]" />
                </div>
                <div>
                  <p className="text-sm font-medium">{trust.provider}</p>
                  <p className="font-mono text-xs text-[var(--color-fg-muted)]">
                    {trust.subject}
                    {trust.environment ? (
                      <> &mdash; env: {trust.environment}</>
                    ) : null}
                  </p>
                </div>
              </div>
              <span className="shrink-0 text-xs text-[var(--color-fg-subtle)]">
                Last verified: {trust.lastVerified}
              </span>
            </div>
          </li>
        ))}
      </ul>

      {/* New trust relationship button — disabled, labelled for AT. */}
      <button
        type="button"
        disabled
        aria-disabled="true"
        aria-describedby="trust-disabled-reason"
        className="inline-flex items-center gap-2 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-surface)] px-4 py-2 text-sm font-medium opacity-60 cursor-not-allowed"
      >
        New trust relationship
      </button>

      {/* Inline flow diagram — text-based; purely informational. */}
      <section aria-label="Authentication flow diagram">
        <h2 className="mb-3 text-sm font-medium text-[var(--color-fg-muted)]">
          How it works
        </h2>
        <div
          className="overflow-x-auto rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-surface)] p-6"
          aria-label="Authentication flow: GHA runner produces an OIDC JWT, which registry-auth validates and exchanges for a short-lived registry token, which the runner uses to push or pull from registry-core."
        >
          {/* SVG flow diagram — decorative, full description above. */}
          <svg
            viewBox="0 0 520 64"
            aria-hidden="true"
            className="w-full max-w-xl"
          >
            <defs>
              <marker
                id="arrowhead"
                markerWidth="7"
                markerHeight="7"
                refX="6"
                refY="3.5"
                orient="auto"
              >
                <polygon points="0 0, 7 3.5, 0 7" className="fill-current text-[var(--color-fg-muted)]" />
              </marker>
            </defs>

            {/* Nodes */}
            <rect x="0" y="16" width="110" height="32" rx="6" className="fill-amber-50 stroke-amber-300 dark:fill-amber-950/40 dark:stroke-amber-700" strokeWidth="1" />
            <text x="55" y="36" textAnchor="middle" className="fill-current text-xs" style={{ fontSize: 11 }}>GHA runner</text>

            <rect x="155" y="16" width="110" height="32" rx="6" className="fill-amber-50 stroke-amber-300 dark:fill-amber-950/40 dark:stroke-amber-700" strokeWidth="1" />
            <text x="210" y="36" textAnchor="middle" className="fill-current text-xs" style={{ fontSize: 11 }}>registry-auth</text>

            <rect x="310" y="16" width="110" height="32" rx="6" className="fill-amber-50 stroke-amber-300 dark:fill-amber-950/40 dark:stroke-amber-700" strokeWidth="1" />
            <text x="365" y="36" textAnchor="middle" className="fill-current text-xs" style={{ fontSize: 11 }}>registry-core</text>

            {/* Arrows */}
            <line x1="110" y1="32" x2="153" y2="32" stroke="currentColor" strokeWidth="1.5" markerEnd="url(#arrowhead)" className="text-[var(--color-fg-muted)]" />
            <text x="132" y="26" textAnchor="middle" style={{ fontSize: 9 }} className="fill-current text-[var(--color-fg-subtle)]">OIDC JWT</text>

            <line x1="265" y1="32" x2="308" y2="32" stroke="currentColor" strokeWidth="1.5" markerEnd="url(#arrowhead)" className="text-[var(--color-fg-muted)]" />
            <text x="287" y="26" textAnchor="middle" style={{ fontSize: 9 }} className="fill-current text-[var(--color-fg-subtle)]">short-lived token</text>
          </svg>

          {/* Text fallback visible below the SVG for keyboard / no-SVG contexts. */}
          <p className="mt-3 font-mono text-xs text-[var(--color-fg-muted)]">
            GHA runner → OIDC JWT → registry-auth → short-lived registry token
            → registry-core
          </p>
        </div>
      </section>
    </div>
  );
}

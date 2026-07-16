import * as React from "react";
import { cn } from "@/lib/utils";
import type { SigningCoverageSummary as Summary } from "@/lib/api/signing-coverage";

// Beacon — SigningCoverageSummary. Four stat cards above the coverage table.
// The "enforced w/ empty allowlist" card uses a warning tone because it is the
// posture soft spot (require_signature on, but ANY signature passes).

interface StatCardProps {
  label: string;
  value: string;
  tone?: "default" | "warning";
}

function StatCard({ label, value, tone = "default" }: StatCardProps): React.ReactElement {
  return (
    <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
      <div className="text-xs font-medium uppercase tracking-[0.14em] text-[var(--color-fg-subtle)]">
        {label}
      </div>
      <div
        className={cn(
          "mt-2 font-display text-2xl font-semibold tabular-nums",
          tone === "warning" && "text-[var(--color-warning)]",
        )}
      >
        {value}
      </div>
    </div>
  );
}

interface SigningCoverageSummaryProps {
  summary: Summary;
}

export function SigningCoverageSummary({
  summary,
}: SigningCoverageSummaryProps): React.ReactElement {
  // workspace_signed_tag_pct is a fraction in [0,1]; render as a whole percent.
  const pct = `${Math.round(summary.workspace_signed_tag_pct * 100)}%`;
  return (
    <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
      <StatCard
        label="Repos requiring signature"
        value={`${summary.repos_require_signature} / ${summary.repo_count}`}
      />
      <StatCard label="Workspace signed-tag coverage" value={pct} />
      <StatCard
        label="Enforced w/ empty allowlist"
        value={String(summary.repos_enforced_empty_allowlist)}
        // Any repo enforcing signatures with an empty allowlist is the soft
        // spot — surface it in warning tone so it reads as an action item.
        tone={summary.repos_enforced_empty_allowlist > 0 ? "warning" : "default"}
      />
      <StatCard label="Repositories" value={String(summary.repo_count)} />
    </div>
  );
}

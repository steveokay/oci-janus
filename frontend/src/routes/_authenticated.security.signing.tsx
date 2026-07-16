// REDESIGN-001 Phase 4.2.e — Security › Signing tab.
//
// Workspace-wide Cosign signing coverage rollup (futures.md "Signing coverage
// rollup"). Per-repo signed-tag %, recent signers, trusted-key allowlist
// health, and require_signature status, from the BFF
// GET /api/v1/signing/coverage. Per-tag verify + the per-repo trusted-key
// editor live on the repository pages; this tab is the read-only rollup and
// drills into per-repo Settings for any change.
import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { FileSignature } from "lucide-react";
import { useSigningCoverage } from "@/lib/api/signing-coverage";
import { SigningCoverageSummary } from "@/components/security/signing-coverage-summary";
import { SigningCoverageTable } from "@/components/security/signing-coverage-table";

export const Route = createFileRoute("/_authenticated/security/signing")({
  component: SigningTab,
});

const WINDOW = 50;

export function SigningTab(): React.ReactElement {
  const { data, isLoading, isError } = useSigningCoverage(WINDOW);

  if (isLoading) {
    return (
      <p className="text-sm text-[var(--color-fg-muted)]">Loading signing coverage…</p>
    );
  }

  if (isError || !data) {
    return (
      <p className="text-sm text-[var(--color-danger)]">
        Failed to load signing coverage. Retry shortly.
      </p>
    );
  }

  // Signer not wired → the whole rollup is moot. Reuse the dashed "not wired"
  // card vocabulary the placeholder established.
  if (!data.signer_enabled) {
    return (
      <section className="rounded-lg border border-dashed border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] p-6 text-center">
        <div className="mx-auto inline-flex size-10 items-center justify-center rounded-md bg-[var(--color-surface)] text-[var(--color-fg-muted)]">
          <FileSignature className="size-5" />
        </div>
        <h2 className="mt-3 font-display text-lg font-medium">Image signing coverage</h2>
        <p className="mx-auto mt-2 max-w-prose text-sm text-[var(--color-fg-muted)]">
          Signing is not wired on this deployment (no signer service configured),
          so there is no coverage to report. Configure the signer service to see
          per-repo signed-tag coverage, recent signers, and allowlist health here.
        </p>
      </section>
    );
  }

  if (data.repos.length === 0) {
    return (
      <p className="text-sm text-[var(--color-fg-muted)]">
        No repositories yet. Coverage appears once repositories exist.
      </p>
    );
  }

  return (
    <div className="space-y-5">
      <SigningCoverageSummary summary={data.summary} />
      <SigningCoverageTable repos={data.repos} />
      <p className="text-xs text-[var(--color-fg-subtle)]">
        Coverage computed over the {data.window} most-recent tags per repository.
      </p>
    </div>
  );
}

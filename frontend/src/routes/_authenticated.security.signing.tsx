// REDESIGN-001 Phase 4.2.e — Security › Signing tab (placeholder).
//
// New tab for this PR. Surfaces workspace-wide Cosign / Notary v2 signing
// posture — coverage % by repo, recent-signer rollup, trusted-key
// allowlist health, and "require_signature" admission status. None of
// that has a workspace-scoped backend yet:
//
//   - signature.ts is per-tag (signer.ListSignatures wrapped per
//     manifest digest).
//   - trusted-keys.ts is per-repo (Phase 2 allowlist + recent-signers
//     picker).
//
// Building the workspace-wide aggregate (a "signing overview" BFF
// rollup) is tracked under futures.md "Admission policy — signed-image
// enforcement" Phase 3 alongside the policy-attestation work. Until
// that ships we render a dashed placeholder so the operator knows the
// tab exists and what to expect — mirrors the MFA placeholder pattern
// in /settings/account.
//
// We do NOT freehand a one-off workspace signing UI here — the per-repo
// signing surface (RepoTrustedKeysSection) is the source of truth today,
// and duplicating it at the workspace level without the rollup
// backend would just be three useTrustedKeys hooks in a loop. Wait
// for the BFF rollup, then revisit.
import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { FileSignature } from "lucide-react";

export const Route = createFileRoute("/_authenticated/security/signing")({
  component: SigningTab,
});

function SigningTab(): React.ReactElement {
  return (
    <section className="rounded-lg border border-dashed border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] p-6 text-center">
      <div className="mx-auto inline-flex size-10 items-center justify-center rounded-md bg-[var(--color-surface)] text-[var(--color-fg-muted)]">
        <FileSignature className="size-5" />
      </div>
      <h2 className="mt-3 font-display text-lg font-medium">
        Image signing coverage
      </h2>
      <p className="mx-auto mt-2 max-w-prose text-sm text-[var(--color-fg-muted)]">
        Image signing coverage lands here — Cosign / Notary v2 visibility
        across the workspace: per-repo signed-tag percentage, recent
        signers, trusted-key allowlist health, and which repos have
        <code className="mx-1 rounded bg-[var(--color-surface)] px-1 py-0.5 text-xs">
          require_signature
        </code>
        turned on. Per-tag verify and the per-repo trusted-key editor
        already exist on the repository pages; this tab is the
        workspace-wide rollup.
      </p>
      <p className="mt-3 text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
        Tracked under futures.md — Signed-image admission, Phase 3
      </p>
    </section>
  );
}

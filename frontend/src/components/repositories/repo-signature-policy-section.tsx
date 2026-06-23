import * as React from "react";
import { ShieldCheck, ShieldAlert } from "lucide-react";
import { toast } from "sonner";
import { AxiosError } from "axios";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorState } from "@/components/ui/error-state";
import { useRepository, useUpdateRepository } from "@/lib/api/repositories";

// RepoSignaturePolicySection — Settings-tab card on the repo detail page.
//
// Toggles the `require_signature` flag on the parent repository (futures.md
// Tier 1 #3). When enabled, services/core's GetManifest fail-closes any pull
// of a manifest digest that registry-signer has no recorded signatures for
// — the OCI client receives 403 DENIED. Pulls of already-signed manifests
// continue to succeed; the only "broken" path is unsigned pulls, which is
// exactly the supply-chain control this flag exists to enforce.
//
// Why this is a separate card from RepoImmutabilitySection: same shape
// (security-relevant flip, single switch, distinct audit semantics) but
// the two policies compose independently — a repo can be immutable but
// unsigned (Trivy/SBOM workflow), or signed but mutable (rolling :latest
// behind cosign). Stacking them as siblings on the Settings tab keeps the
// operator's mental model clean.
//
// SECURITY: the BFF gates `PATCH /repositories/{org}/{repo}` with
// `require_signature` on repo admin/owner. The FE doesn't re-validate
// (the server is authoritative); we just optimistically render the
// switch as enabled and let the 403 path show a toast on click.
//
// Phase 1 scope: ANY signature passes. A Phase 2 follow-up adds a
// per-repo trusted-key allowlist so unsigned-by-an-approved-key pulls
// also fail closed. Until then, an operator who turns this on must
// also lock down which Cosign identities can sign for the org —
// typically via Fulcio OIDC issuer claims, not enforced here.

interface RepoSignaturePolicySectionProps {
  org: string;
  repo: string;
}

export function RepoSignaturePolicySection({
  org,
  repo,
}: RepoSignaturePolicySectionProps): React.ReactElement {
  const { data, isLoading, isError, refetch } = useRepository(org, repo);
  const update = useUpdateRepository();
  const required = data?.require_signature ?? false;

  if (isError) {
    return (
      <ErrorState
        title="Couldn't load repository settings"
        description="The management API didn't answer. Retry, or check the BFF logs."
        onRetry={() => void refetch()}
      />
    );
  }

  async function toggle(next: boolean): Promise<void> {
    try {
      await update.mutateAsync({ org, repo, require_signature: next });
      toast.success(
        next
          ? "Signed-image admission enabled — unsigned pulls will be rejected."
          : "Signed-image admission disabled — unsigned pulls will succeed.",
      );
    } catch (e) {
      const code = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        code === 403
          ? "Repository admin role required."
          : "Couldn't update signature policy. Check the BFF logs.",
      );
    }
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-start justify-between gap-3">
          <div className="space-y-1">
            <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              Signed-image admission
            </CardDescription>
            <p className="text-xs text-[var(--color-fg-muted)]">
              When on, pulls of manifests that registry-signer has no
              signature for are rejected with{" "}
              <code className="font-mono text-[10px]">403 DENIED</code>.
              Sign images with{" "}
              <code className="font-mono text-[10px]">cosign sign</code>{" "}
              before turning this on, or every pull breaks.
            </p>
          </div>
          {isLoading ? (
            <Skeleton className="size-4 rounded-full" />
          ) : required ? (
            <Badge tone="success">
              <ShieldCheck className="size-3" /> Signed-only
            </Badge>
          ) : (
            <Badge tone="neutral">
              <ShieldAlert className="size-3" /> Open
            </Badge>
          )}
        </div>
      </CardHeader>
      <CardContent className="pt-0">
        {isLoading ? (
          <Skeleton className="h-10 w-full" />
        ) : (
          <label className="flex cursor-pointer items-center justify-between gap-3 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3 py-2">
            <span className="flex flex-col gap-0.5">
              <span className="text-sm font-medium text-[var(--color-fg)]">
                Require a signature on every pull
              </span>
              <span className="text-xs text-[var(--color-fg-muted)]">
                Phase 1 — ANY signature passes. A per-repo trusted-key
                allowlist is a planned Phase 2 follow-up.
              </span>
            </span>
            <Switch
              checked={required}
              disabled={update.isPending}
              onCheckedChange={(next) => void toggle(next)}
              aria-label="Require signed images"
            />
          </label>
        )}
        {required ? (
          <p className="mt-3 flex items-start gap-2 rounded-md border border-[var(--color-accent)]/30 bg-[var(--color-accent-subtle)]/30 px-3 py-2 text-xs text-[var(--color-fg)]">
            <ShieldCheck className="mt-0.5 size-3.5 shrink-0 text-[var(--color-accent)]" />
            <span>
              Pulls of unsigned manifests in{" "}
              <code className="font-mono">{org}/{repo}</code> will return{" "}
              <code className="font-mono">403 DENIED</code>. Sign first
              with <code className="font-mono">cosign sign</code>, or turn
              this off to restore open access.
            </span>
          </p>
        ) : null}
      </CardContent>
    </Card>
  );
}

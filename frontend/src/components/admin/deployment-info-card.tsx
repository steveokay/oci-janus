// REDESIGN-001 Phase 4.2.d — DeploymentInfoCard.
//
// Read-only display of the deployment's mode + version + posture flags.
// Sources are the public /api/v1/deployment-info (mode + version, Phase 4.1)
// and the caller's claims (tenant id). Used by:
//   - Settings › Platform tab (multi-mode + global-admin operators)
//   - Settings › Workspace tab (single-mode operators — there's no
//     Platform tab in single mode, so deployment info collapses here)
//
// There's intentionally no "edit" surface — DEPLOYMENT_MODE / mTLS posture
// are deployment-time configuration baked into env vars and Helm values.
// Changing them means redeploying the control plane.
import * as React from "react";
import { Server, ShieldCheck, Hash, Activity } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorState } from "@/components/ui/error-state";
import { useDeploymentInfo } from "@/lib/api/deployment-info";
import { authStore } from "@/lib/auth/store";

export function DeploymentInfoCard(): React.ReactElement {
  const { data, isLoading, isError, refetch } = useDeploymentInfo();
  // Tenant id is taken from the JWT claims rather than a separate /me
  // round-trip; it's the bootstrap tenant in single mode and the
  // current tenant in multi mode. Same value either way.
  const tenantId = authStore.getClaims()?.tenant_id ?? null;

  if (isError) {
    return (
      <section id="deployment" className="scroll-mt-24">
        <ErrorState
          title="Couldn't load deployment info"
          description="The /api/v1/deployment-info endpoint didn't answer. Retry, or check the BFF logs."
          onRetry={() => void refetch()}
        />
      </section>
    );
  }

  return (
    <section id="deployment" className="scroll-mt-24">
      <Card>
        <CardHeader>
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Posture
          </CardDescription>
          <h2 className="flex items-center gap-2 font-display text-xl font-medium">
            <Server className="size-4 text-[var(--color-fg-muted)]" />
            Deployment info
          </h2>
          <p className="text-sm text-[var(--color-fg-muted)]">
            Read-only snapshot of how this control plane is running.
            DEPLOYMENT_MODE and TLS posture are baked into the deployment
            config — change them by redeploying, not from here.
          </p>
        </CardHeader>
        <CardContent>
          <dl className="grid grid-cols-1 gap-x-6 gap-y-4 sm:grid-cols-2">
            <InfoRow
              icon={<Activity className="size-4" />}
              label="Mode"
              value={
                isLoading ? (
                  <Skeleton className="h-5 w-20" />
                ) : (
                  <Badge tone={data?.deployment_mode === "single" ? "neutral" : "accent"}>
                    {data?.deployment_mode ?? "unknown"}
                  </Badge>
                )
              }
              hint={
                data?.deployment_mode === "single"
                  ? "One tenant. Workspace admin == effective platform admin."
                  : data?.deployment_mode === "multi"
                    ? "Multi-tenant. Platform admin gate is required for cross-tenant surfaces."
                    : undefined
              }
            />
            <InfoRow
              icon={<Hash className="size-4" />}
              label="Version"
              value={
                isLoading ? (
                  <Skeleton className="h-5 w-24" />
                ) : (
                  <code className="font-mono text-sm">{data?.version ?? "—"}</code>
                )
              }
              hint='Build tag injected at compile time. "dev" means a local build.'
            />
            <InfoRow
              icon={<Server className="size-4" />}
              label="Tenant ID"
              value={
                tenantId ? (
                  <code className="font-mono text-xs break-all">{tenantId}</code>
                ) : (
                  <span className="text-[var(--color-fg-muted)]">—</span>
                )
              }
              hint={
                data?.deployment_mode === "single"
                  ? "The single bootstrap tenant — this id is fixed for the lifetime of this deployment."
                  : "Your currently-active tenant."
              }
            />
            <InfoRow
              icon={<ShieldCheck className="size-4" />}
              label="TLS"
              value={
                // Neutral informational text, NOT a success badge:
                // /api/v1/deployment-info reports no TLS field, so the FE
                // must not assert a posture ("HTTPS termination" in green)
                // the API doesn't actually report — a misconfigured plain-
                // HTTP deployment would have shown the same green badge.
                <span className="text-sm text-[var(--color-fg-muted)]">
                  TLS is terminated at the gateway — see infra/docker-compose
                  or your ingress config.
                </span>
              }
              hint="Internal mTLS posture is set per service via MTLS_REQUIRED; not exposed here."
            />
          </dl>
        </CardContent>
      </Card>
    </section>
  );
}

// InfoRow — vertical label/value pair with an inline icon. Local primitive
// because it's only used inside this card and the spacing is tuned to the
// 2-col dl layout above.
function InfoRow({
  icon,
  label,
  value,
  hint,
}: {
  icon: React.ReactNode;
  label: string;
  value: React.ReactNode;
  hint?: string;
}): React.ReactElement {
  return (
    <div className="flex flex-col gap-1">
      <dt className="flex items-center gap-1.5 text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
        <span className="text-[var(--color-fg-muted)]">{icon}</span>
        {label}
      </dt>
      <dd className="text-sm text-[var(--color-fg)]">{value}</dd>
      {hint ? (
        <p className="text-xs text-[var(--color-fg-muted)]">{hint}</p>
      ) : null}
    </div>
  );
}

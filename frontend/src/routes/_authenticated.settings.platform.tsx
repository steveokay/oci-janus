// REDESIGN-001 Phase 4.2.d — Settings › Platform tab.
//
// Cross-tenant + infrastructure surfaces for the deployment. Only renders
// in multi-mode deployments with a global-admin caller (parent route
// already enforces both gates on tab visibility; this file adds a route
// guard for direct URL access so a non-global-admin can't hit
// /settings/platform via a bookmark).
//
// Sections (in display order):
//   #tenants     — Tenants table + plan breakdown + dialogs
//   #scanner     — Scanner adapter health + grid + test scan
//   #gc          — GC schedule + run history + run-now
//   #retention   — Retention run history
//   #deployment  — Deployment mode + version + posture
//   #sso         — SSO read-only note (editor not built yet — RM-003 keeps
//                  SSO in deployment config)
//
// The legacy /admin/scanner and /admin/tenants routes 301 here with the
// matching hash so bookmarks keep working.
import * as React from "react";
import { createFileRoute, redirect } from "@tanstack/react-router";
import { ShieldAlert, KeyRound } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { TenantsSection } from "@/components/admin/tenants-section";
import { ScannerAdaptersSection } from "@/components/admin/scanner/scanner-adapters-section";
import { GCCard } from "@/components/admin/gc-card";
import { RetentionCard } from "@/components/admin/retention-card";
import { DeploymentInfoCard } from "@/components/admin/deployment-info-card";
import { queryClient } from "@/lib/query";
import {
  abilitiesKeys,
  type AbilitiesResponse,
} from "@/lib/api/abilities";

// Route guard: direct URL hits go to /settings/account when the caller
// isn't a global admin. Parent route already hides the tab from the rail
// for the same callers, so this fires only on bookmark/URL access.
//
// beforeLoad is synchronous so we can't call useAbilities() here. Read
// the React Query cache directly — by the time the Settings parent route
// has mounted, the abilities query is already in-flight via the parent's
// useAbilities() call. If the cache is cold (cold tab opening
// /settings/platform directly), we err on the side of safety and bounce
// to /settings/account; a global admin can retry once the cache warms.
export const Route = createFileRoute("/_authenticated/settings/platform")({
  beforeLoad: () => {
    const abilities = queryClient.getQueryData<AbilitiesResponse>(
      abilitiesKeys.all,
    );
    if (!abilities?.is_global_admin) {
      throw redirect({ to: "/settings/account", replace: true });
    }
  },
  component: PlatformTab,
});

function PlatformTab(): React.ReactElement {
  return (
    <div className="space-y-8">
      {/* Quiet platform-admin banner — softer than the old /admin pages
          because the Settings tab system already telegraphs the role. We
          keep it because operating on cross-tenant state is a different
          posture than the rest of Settings. */}
      <div className="flex items-center gap-3 rounded-lg border border-[var(--color-highlight)]/30 bg-[var(--color-highlight)]/5 px-4 py-3">
        <ShieldAlert className="size-4 shrink-0 text-[var(--color-highlight)]" />
        <p className="min-w-0 text-xs text-[var(--color-fg-muted)]">
          Platform-admin surface. Actions here affect every tenant on this
          control plane.
        </p>
      </div>

      <TenantsSection />
      <ScannerAdaptersSection />
      {/* GC + Retention are scoped sections rather than dedicated #ids
          because the underlying cards already render their own headers;
          we just give the wrapper an id for hash navigation. */}
      <section id="gc" className="space-y-4 scroll-mt-24">
        <GCCard />
      </section>
      <section id="retention" className="space-y-4 scroll-mt-24">
        <RetentionCard />
      </section>
      <DeploymentInfoCard />
      <SSOReadOnlySection />
    </div>
  );
}

// SSO is read-only in single mode (Workspace tab) and would be the
// editable surface here in multi mode — but the editor doesn't exist yet.
// Per RM-003/004 SSO is configured in deployment files in both modes
// today; the multi-mode editor lands in a future phase. Card lives at the
// bottom of the Platform tab so its presence telegraphs "this is where
// it'll live" without overpromising.
function SSOReadOnlySection(): React.ReactElement {
  return (
    <section id="sso" className="scroll-mt-24">
      <Card>
        <CardHeader>
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Sign-in
          </CardDescription>
          <div className="flex items-center justify-between gap-2">
            <h2 className="flex items-center gap-2 font-display text-xl font-medium">
              <KeyRound className="size-4 text-[var(--color-fg-muted)]" />
              Single sign-on
            </h2>
            <Badge tone="neutral" className="text-[10px]">Read-only</Badge>
          </div>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-[var(--color-fg-muted)]">
            SSO providers (Google, GitHub, Microsoft, generic OIDC, SAML 2.0)
            are currently configured in the deployment's environment / Helm
            values. An editable multi-tenant SSO surface for global admins
            lands in a follow-up phase. Until then, rotate secrets or add
            providers by updating the deployment config and redeploying.
          </p>
        </CardContent>
      </Card>
    </section>
  );
}

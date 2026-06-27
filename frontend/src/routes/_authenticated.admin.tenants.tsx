// REDESIGN-001 Phase 4.2.d — /admin/tenants is now a redirect.
//
// Tenant CRUD only exists in multi mode (single mode has, by definition,
// one tenant and no tenant management surface). The route now:
//   - multi mode  → /settings/platform#tenants  (global-admin gated)
//   - single mode → /settings/workspace         (no tenants section at all;
//                   we drop the hash because there's nothing to anchor to,
//                   and the workspace tab is the right landing for any
//                   workspace-admin who used to bookmark /admin/tenants)
//
// Deployment mode read from React Query cache; if cold, fall back to
// /settings/ and let the index redirect land the caller appropriately.
import { createFileRoute, redirect } from "@tanstack/react-router";
import { queryClient } from "@/lib/query";
import type { DeploymentInfo } from "@/lib/api/deployment-info";

export const Route = createFileRoute("/_authenticated/admin/tenants")({
  beforeLoad: () => {
    const info = queryClient.getQueryData<DeploymentInfo>(["deployment-info"]);
    if (info?.deployment_mode === "multi") {
      throw redirect({
        to: "/settings/platform",
        hash: "tenants",
        replace: true,
      });
    }
    throw redirect({ to: "/settings/workspace", replace: true });
  },
});

// REDESIGN-001 Phase 4.2.d — /admin/scanner is now a redirect.
//
// The scanner-adapter UI lives inside the unified Settings IA:
//   - multi mode  → /settings/platform#scanner   (global-admin gated)
//   - single mode → /settings/workspace#scanner  (workspace-admin gated)
//
// We keep this route as a thin redirect (rather than deleting) so external
// bookmarks, the audit-log activity feed, and Docker compose docs that
// linked to /admin/scanner continue to land somewhere sensible.
//
// Deployment mode is read from the React Query cache. By the time any
// authenticated route loads, `_authenticated` parent route has already
// resolved /api/v1/deployment-info via the sidebar's useDeploymentInfo()
// call, so the cache is warm. If it isn't (cold redirect from a fresh
// tab), we fall back to /settings/ — the parent route will land the
// caller on /settings/account and they navigate from there.
import { createFileRoute, redirect } from "@tanstack/react-router";
import { queryClient } from "@/lib/query";
import type { DeploymentInfo } from "@/lib/api/deployment-info";

export const Route = createFileRoute("/_authenticated/admin/scanner")({
  beforeLoad: () => {
    const info = queryClient.getQueryData<DeploymentInfo>(["deployment-info"]);
    const target =
      info?.deployment_mode === "multi"
        ? "/settings/platform"
        : "/settings/workspace";
    // hash isn't a first-class field on TanStack's redirect ({to,...}) —
    // pass it via the `hash` option so the section anchor scrolls in.
    throw redirect({ to: target, hash: "scanner", replace: true });
  },
});

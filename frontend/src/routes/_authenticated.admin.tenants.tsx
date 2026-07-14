// /admin/tenants — legacy redirect.
//
// The platform is single-tenant only (ADR-0031) — there is one tenant per
// deployment and no tenant-management surface. This route is kept as a thin
// redirect (rather than deleted) so any old bookmark to /admin/tenants lands
// on the Workspace settings tab instead of a 404.
import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/_authenticated/admin/tenants")({
  beforeLoad: () => {
    throw redirect({ to: "/settings/workspace", replace: true });
  },
});

// /admin/scanner — legacy redirect.
//
// The scanner-adapter UI lives inside the unified Settings IA on the Scanning
// tab (/settings/scanning). We keep this route as a thin redirect (rather than
// deleting it) so external bookmarks, the audit-log activity feed, and Docker
// compose docs that linked to /admin/scanner continue to land somewhere
// sensible. The platform is single-tenant only (ADR-0031), so there is no
// mode branch — the target is always the Scanning tab.
import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/_authenticated/admin/scanner")({
  beforeLoad: () => {
    throw redirect({ to: "/settings/scanning", replace: true });
  },
});

// REDESIGN-001 Phase 4.2.b — /profile collapses into Settings › Account.
//
// Profile + password + personal API keys all moved to /settings/account in
// the unified Settings IA. /profile is kept as a redirect so the topbar
// dropdown ("Your profile"), existing bookmarks, and the /api-keys hub's
// "Manage profile" link continue to land somewhere sensible without us
// having to chase every callsite in a single PR.
//
// A future cleanup (likely 4.2.c or 4.5) will rewrite those callsites to
// link to /settings/account directly and let this file go away.
import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/_authenticated/profile")({
  // beforeLoad fires before component mount so browser back/forward and
  // shared links land on /settings/account immediately — no flash.
  beforeLoad: () => {
    throw redirect({ to: "/settings/account", replace: true });
  },
});

// UI cleanup 2026-07-05 — /settings/account collapses into /profile.
//
// Personal account state (identity, password, personal API keys, MFA, active
// sessions) moved to the top-level /profile page when Profile and Settings
// were split into separate sidebar items. Notification preferences moved to
// /settings/notifications. This route is kept as a redirect so existing
// bookmarks + deep links to /settings/account land somewhere sensible.
import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/_authenticated/settings/account")({
  // beforeLoad fires before component mount so browser back/forward and
  // shared links land on /profile immediately — no flash.
  beforeLoad: () => {
    throw redirect({ to: "/profile", replace: true });
  },
});

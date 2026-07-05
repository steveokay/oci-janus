// /settings index → /settings/workspace redirect.
//
// /settings on its own has no content of its own; the parent route renders
// the tab rail and the active tab content via <Outlet/>. So landing on a
// bare /settings has to redirect to a concrete tab. Workspace is the default
// — it's the first tab and the main configuration surface for the operator
// (the bootstrap/global admin) who reaches Settings. Notification prefs (the
// one tab every non-admin also sees) is one click away.
import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/_authenticated/settings/")({
  // beforeLoad runs before the route component mounts, so the redirect
  // happens during navigation rather than as a side effect of a render —
  // browser back/forward + share links land on /settings/workspace directly.
  beforeLoad: () => {
    throw redirect({ to: "/settings/workspace", replace: true });
  },
});

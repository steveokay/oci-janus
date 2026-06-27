// REDESIGN-001 Phase 4.2.b — /settings index → /settings/account redirect.
//
// /settings on its own has no content of its own; the parent route renders
// the tab rail and the active tab content via <Outlet/>. So landing on a
// bare /settings has to redirect to a concrete tab. Account is the default
// because every authenticated caller has it (Workspace/Platform are role-gated).
import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/_authenticated/settings/")({
  // beforeLoad runs before the route component mounts, so the redirect
  // happens during navigation rather than as a side effect of a render —
  // browser back/forward + share links land on /settings/account directly.
  beforeLoad: () => {
    throw redirect({ to: "/settings/account", replace: true });
  },
});

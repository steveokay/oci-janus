// REDESIGN-001 Phase 4.2.e — /security index → /security/overview redirect.
//
// /security on its own has no content of its own; the parent route renders
// the always-visible posture row + tab rail and the active tab content via
// <Outlet/>. So landing on a bare /security has to redirect to a concrete
// tab. Overview is the default because it's the lightest read (no table
// fetch) and gives the operator the severity breakdown immediately.
//
// The sidebar link still points at /security — this redirect is what makes
// that link land on a concrete sub-route without having to change the
// sidebar wiring.
import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/_authenticated/security/")({
  // beforeLoad runs before the route component mounts, so the redirect
  // happens during navigation rather than as a side effect of a render —
  // browser back/forward + share links land on /security/overview directly.
  beforeLoad: () => {
    throw redirect({ to: "/security/overview", replace: true });
  },
});

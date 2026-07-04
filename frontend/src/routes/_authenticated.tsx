import * as React from "react";
import { createFileRoute, Outlet, redirect } from "@tanstack/react-router";
import { AppShell } from "@/components/shell/app-shell";
import { authStore } from "@/lib/auth/store";

// Beacon — `_authenticated` layout route.
// Any file under `routes/_authenticated/` automatically renders inside the
// AppShell and requires a valid JWT. We check the auth store synchronously
// in `beforeLoad`; the silent-refresh scheduler keeps it fresh while the
// user is here.
export const Route = createFileRoute("/_authenticated")({
  beforeLoad: ({ location }) => {
    if (!authStore.getToken()) {
      throw redirect({
        to: "/login",
        // Preserve where the user was headed so login.tsx can bounce back
        // after a successful sign-in (login validates ?from= and only
        // honors internal absolute paths — see safeInternalPath there).
        search: { from: location.pathname },
      });
    }
  },
  component: AuthenticatedLayout,
});

function AuthenticatedLayout(): React.ReactElement {
  return (
    <AppShell>
      <Outlet />
    </AppShell>
  );
}

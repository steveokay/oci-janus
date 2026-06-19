import * as React from "react";
import { Outlet, createRootRoute } from "@tanstack/react-router";
import { Toaster } from "sonner";

// Beacon — root route. Holds anything global: the sonner toaster + dev tools.
// Devtools are lazy-loaded so the prod bundle doesn't carry them.
const TanStackRouterDevtools = import.meta.env.DEV
  ? React.lazy(() =>
      import("@tanstack/router-devtools").then((m) => ({
        default: m.TanStackRouterDevtools,
      })),
    )
  : (): null => null;

export const Route = createRootRoute({
  component: RootComponent,
});

function RootComponent(): React.ReactElement {
  return (
    <>
      <Outlet />
      <Toaster
        position="top-right"
        toastOptions={{
          className:
            "!bg-[var(--color-surface-2)] !text-[var(--color-fg)] !border !border-[var(--color-border)]",
        }}
      />
      <React.Suspense>
        <TanStackRouterDevtools position="bottom-right" />
      </React.Suspense>
    </>
  );
}

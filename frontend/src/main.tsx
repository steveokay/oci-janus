import * as React from "react";
import ReactDOM from "react-dom/client";
import { RouterProvider, createRouter } from "@tanstack/react-router";
import { QueryClientProvider } from "@tanstack/react-query";
import { ReactQueryDevtools } from "@tanstack/react-query-devtools";

// Fonts — packaged at build time so we don't depend on Google Fonts at
// runtime. FE-SEC-013/014 require us to keep external requests out.
import "@fontsource-variable/inter";
import "@fontsource-variable/fraunces";
import "@fontsource/jetbrains-mono/400.css";
import "@fontsource/jetbrains-mono/500.css";

import "./index.css";

import { routeTree } from "./routeTree.gen";
import { queryClient } from "./lib/query";
import { initTheme } from "./lib/theme";
import { startRefreshScheduler } from "./lib/auth/refresh-scheduler";

// Wire the theme + refresh scheduler before the first React render so the
// initial paint reflects the user's stored theme without a flash.
initTheme();
startRefreshScheduler();

const router = createRouter({
  routeTree,
  defaultPreload: "intent",
  defaultPreloadStaleTime: 0,
});

// TanStack Router type augmentation — required for typed Link.
declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
      {import.meta.env.DEV ? (
        <ReactQueryDevtools initialIsOpen={false} />
      ) : null}
    </QueryClientProvider>
  </React.StrictMode>,
);

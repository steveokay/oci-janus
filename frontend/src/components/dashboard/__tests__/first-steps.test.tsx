import * as React from "react";
import { act, render, screen } from "@testing-library/react";
import { describe, test, expect } from "vitest";
import {
  createRouter,
  createMemoryHistory,
  RouterProvider,
  createRoute,
  createRootRoute,
} from "@tanstack/react-router";
import { FirstSteps } from "../first-steps";
import type { Workspace } from "@/lib/api/workspace";

// ---------------------------------------------------------------------------
// FirstSteps is the DSGN-005 first-run dashboard card stack. The route
// owns data-fetching + auto-navigate; this component is purely visual.
// Tests verify:
//   - All four cards render with workspace host + plan badge.
//   - docker login command + create-API-key affordance are present.
//   - docker tag + docker push commands are present.
//   - Polling indicator flips text + tone when firstRepoSeen=true.
//   - Local-dev host adds the "dev stack: HTTP" comment to login.
// ---------------------------------------------------------------------------

// FirstSteps contains a TanStack <Link to="/api-keys"> in LoginCard, so we
// have to mount it inside a RouterProvider for the link context.
async function renderWithRouter(ui: React.ReactElement): Promise<void> {
  const rootRoute = createRootRoute({ component: () => ui });
  const indexRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/",
    component: () => null,
  });
  const apiKeysRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/api-keys",
    component: () => null,
  });
  const routeTree = rootRoute.addChildren([indexRoute, apiKeysRoute]);
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  await router.load();
  await act(async () => {
    render(<RouterProvider router={router} />);
  });
}

const customWorkspace: Workspace = {
  tenant_id: "tenant-1",
  name: "Acme",
  slug: "acme",
  plan: "pro",
  host: "registry.acme.com",
  host_is_custom: true,
  domains: [],
  created_at: "2026-06-01T00:00:00Z",
};

const localWorkspace: Workspace = {
  tenant_id: "tenant-2",
  name: "Dev",
  slug: "dev",
  plan: "free",
  host: "registry.localhost",
  host_is_custom: false,
  domains: [],
  created_at: "2026-06-01T00:00:00Z",
};

describe("FirstSteps", () => {
  test("renders all four cards plus plan + custom-host badges", async () => {
    await renderWithRouter(
      <FirstSteps workspace={customWorkspace} firstRepoSeen={false} />,
    );

    // 1. Endpoint card
    expect(screen.getByText("Your registry endpoint")).toBeInTheDocument();
    expect(screen.getByText("registry.acme.com")).toBeInTheDocument();
    expect(screen.getByText("pro")).toBeInTheDocument();
    expect(screen.getByText("custom host")).toBeInTheDocument();

    // 2. Login card + the "create an API key" affordance.
    expect(
      screen.getByText("Authenticate your docker CLI"),
    ).toBeInTheDocument();
    const apiKeyLink = screen.getByRole("link", { name: /create an api key/i });
    expect(apiKeyLink).toHaveAttribute("href", "/api-keys");
    // docker login command surfaces the host. No --plain-http comment for
    // a non-local-looking host. We use getAllByText because the command
    // text appears both as the visible <span> AND in the copy button's
    // accessible name — both are legitimate, so just confirm at least
    // one match exists.
    expect(
      screen.getAllByText(/docker login registry\.acme\.com -u <user>$/).length,
    ).toBeGreaterThan(0);

    // 3. Push card — tag + push commands present.
    expect(screen.getByText("Push your first image")).toBeInTheDocument();
    expect(
      screen.getAllByText(
        /docker tag local-image:latest registry\.acme\.com\/your-org\/your-image:1\.0\.0/,
      ).length,
    ).toBeGreaterThan(0);
    expect(
      screen.getAllByText(
        /docker push registry\.acme\.com\/your-org\/your-image:1\.0\.0/,
      ).length,
    ).toBeGreaterThan(0);

    // 4. Polling indicator — waiting state by default.
    expect(
      screen.getByText("Waiting for your first image…"),
    ).toBeInTheDocument();
  });

  test("local-dev host appends the dev stack: HTTP comment on login", async () => {
    await renderWithRouter(
      <FirstSteps workspace={localWorkspace} firstRepoSeen={false} />,
    );
    expect(
      screen.getAllByText(
        /docker login registry\.localhost -u <user> # dev stack: HTTP/,
      ).length,
    ).toBeGreaterThan(0);
  });

  test("polling indicator flips to success state when firstRepoSeen=true", async () => {
    await renderWithRouter(
      <FirstSteps workspace={customWorkspace} firstRepoSeen={true} />,
    );
    expect(screen.getByText("First image received.")).toBeInTheDocument();
    expect(screen.getByText("Opening your repositories…")).toBeInTheDocument();
    // The waiting copy must be gone — we don't want both states visible.
    expect(
      screen.queryByText("Waiting for your first image…"),
    ).not.toBeInTheDocument();
  });

  test("falls back to registry.localhost when workspace is unwired", async () => {
    // /workspace/me returning null happens on management deployments
    // without TENANT_GRPC_ADDR — we still need a copyable host so the
    // walkthrough isn't useless. Confirm the fallback host shows up.
    await renderWithRouter(
      <FirstSteps workspace={null} firstRepoSeen={false} />,
    );
    expect(screen.getByText("registry.localhost")).toBeInTheDocument();
  });
});

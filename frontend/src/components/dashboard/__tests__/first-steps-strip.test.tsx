import { act, render, screen } from "@testing-library/react";
import { describe, test, expect } from "vitest";
import { FirstStepsStrip } from "../first-steps-strip";
import type { Workspace } from "@/lib/api/workspace";

// ---------------------------------------------------------------------------
// FirstStepsStrip is the DSGN-005 v2 hybrid first-run affordance. The
// stat row stays visible; this strip slots in directly below it when
// the tenant has zero repositories. The route owns data-fetching +
// auto-navigate; this component is purely visual.
//
// Tests verify:
//   - Endpoint host + custom-host badge render in the eyebrow.
//     (Plan badge removed in Phase 2.4 / RM-006 — no billing in
//     single-mode default. New case below pins the removal.)
//   - "Create an API key" + "Read the docs" links surface.
//   - All three commands (login / tag / push) render verbatim.
//   - Replace-org-image hint renders.
//   - Polling indicator flips to a success message on firstRepoSeen=true.
//   - Falls back to registry.localhost when workspace is unwired.
// ---------------------------------------------------------------------------

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

describe("FirstStepsStrip", () => {
  test("renders endpoint, badges, action links, all three commands, hint", async () => {
    await act(async () => {
      render(
        <FirstStepsStrip
          workspace={customWorkspace}
          firstRepoSeen={false}
        />,
      );
    });

    // Eyebrow + endpoint identity.
    expect(screen.getByText("Get started")).toBeInTheDocument();
    expect(screen.getByText("registry.acme.com")).toBeInTheDocument();
    // Phase 2.4 / RM-006 — plan badge removed; assert it's gone even
    // though the mock workspace still ships plan: "pro" (the BFF still
    // serves the field per HD-004).
    expect(screen.queryByText("pro")).not.toBeInTheDocument();
    expect(screen.getByText("custom host")).toBeInTheDocument();

    // Action links.
    const apiKey = screen.getByRole("link", { name: /create an api key/i });
    expect(apiKey).toHaveAttribute("href", "/api-keys");
    const docs = screen.getByRole("link", { name: /read the docs/i });
    expect(docs).toHaveAttribute(
      "href",
      "https://github.com/steveokay/oci-janus/blob/main/docs/SELF-HOSTING.md",
    );
    expect(docs).toHaveAttribute("target", "_blank");

    // Tagline.
    expect(
      screen.getByText("Three commands to your first push."),
    ).toBeInTheDocument();

    // All three commands surface. Non-local host → no dev-stack comment.
    expect(
      screen.getByText("docker login registry.acme.com -u <user>"),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "docker tag local-image:latest registry.acme.com/your-org/your-image:1.0.0",
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "docker push registry.acme.com/your-org/your-image:1.0.0",
      ),
    ).toBeInTheDocument();

    // Hint about replacing the placeholder names.
    expect(screen.getByText("your-org/your-image")).toBeInTheDocument();

    // Polling — waiting state by default.
    expect(
      screen.getByText("Polling for your first image…"),
    ).toBeInTheDocument();
  });

  test("local-dev host appends the dev stack: HTTP comment on login", async () => {
    await act(async () => {
      render(
        <FirstStepsStrip
          workspace={localWorkspace}
          firstRepoSeen={false}
        />,
      );
    });
    expect(
      screen.getByText(
        "docker login registry.localhost -u <user> # dev stack: HTTP",
      ),
    ).toBeInTheDocument();
  });

  test("polling indicator flips to success state when firstRepoSeen=true", async () => {
    await act(async () => {
      render(
        <FirstStepsStrip
          workspace={customWorkspace}
          firstRepoSeen={true}
        />,
      );
    });
    expect(screen.getByText("First image received.")).toBeInTheDocument();
    expect(
      screen.getByText("Opening your repositories…"),
    ).toBeInTheDocument();
    // Waiting copy must be gone — we don't want both states visible.
    expect(
      screen.queryByText("Polling for your first image…"),
    ).not.toBeInTheDocument();
  });

  test("falls back to registry.localhost when workspace is unwired", async () => {
    // /workspace/me returning null happens on management deployments
    // without TENANT_GRPC_ADDR — we still need a copyable host so the
    // strip isn't useless. Confirm the fallback host shows up in both
    // the eyebrow and the rendered commands.
    await act(async () => {
      render(
        <FirstStepsStrip workspace={null} firstRepoSeen={false} />,
      );
    });
    expect(screen.getByText("registry.localhost")).toBeInTheDocument();
    expect(
      screen.getByText(
        "docker login registry.localhost -u <user> # dev stack: HTTP",
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "docker push registry.localhost/your-org/your-image:1.0.0",
      ),
    ).toBeInTheDocument();
  });
});

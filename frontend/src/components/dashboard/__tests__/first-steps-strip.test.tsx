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
//   - Endpoint host + plan + custom-host badges render in the eyebrow.
//   - "Read the docs" link points at the SELF-HOSTING doc on GitHub.
//   - Both docker login + docker push commands surface verbatim.
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
  test("renders endpoint, plan + custom-host badges, docs link, and both commands", async () => {
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
    expect(screen.getByText("pro")).toBeInTheDocument();
    expect(screen.getByText("custom host")).toBeInTheDocument();

    // Docs link — opens the SELF-HOSTING doc in a new tab.
    const docs = screen.getByRole("link", { name: /read the docs/i });
    expect(docs).toHaveAttribute(
      "href",
      "https://github.com/steveokay/oci-janus/blob/main/docs/SELF-HOSTING.md",
    );
    expect(docs).toHaveAttribute("target", "_blank");

    // Both commands surface. Non-local host → no dev-stack comment.
    expect(
      screen.getByText("docker login registry.acme.com -u <user>"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("docker push registry.acme.com/your-org/image:tag"),
    ).toBeInTheDocument();

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
      screen.getByText("docker push registry.localhost/your-org/image:tag"),
    ).toBeInTheDocument();
  });
});

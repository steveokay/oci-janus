import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RepoTransferSection } from "../repo-transfer-section";

// Tests for the General-section Transfer editor (repo transfer feature, PR D).
// Behaviours:
//   - Lists candidate destination orgs (every org except the current one).
//   - The Transfer button stays disabled until a destination is picked.
//   - Confirming the dialog fires the mutation with { org, repo, dest_org } and,
//     on success, navigates to the repo's new org.
//
// The repositories + orgs API, router navigation, and toast are mocked so no
// HTTP or routing is touched.

const mockMutate = vi.fn();
const mockNavigate = vi.fn();
let mockPending = false;

vi.mock("@/lib/api/repositories", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api/repositories")>(
      "@/lib/api/repositories",
    );
  return {
    ...actual,
    useTransferRepository: () => ({
      mutateAsync: mockMutate,
      mutate: mockMutate,
      isPending: mockPending,
      error: null,
      reset: vi.fn(),
    }),
  };
});

vi.mock("@/lib/api/orgs", () => ({
  useOrgs: () => ({
    data: {
      orgs: [
        { org_id: "1", org: "acme", repo_count: 3, storage_used_bytes: 0 },
        { org_id: "2", org: "beta", repo_count: 1, storage_used_bytes: 0 },
        { org_id: "3", org: "gamma", repo_count: 0, storage_used_bytes: 0 },
      ],
    },
    isLoading: false,
  }),
}));

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => mockNavigate,
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() },
}));

function renderSection() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <RepoTransferSection org="acme" repo="api" />
    </QueryClientProvider>,
  );
}

describe("RepoTransferSection", () => {
  beforeEach(() => {
    mockMutate.mockReset();
    mockMutate.mockResolvedValue({ roles_rewritten: 2 });
    mockNavigate.mockReset();
    mockPending = false;
  });

  it("keeps Transfer disabled until a destination is picked", () => {
    renderSection();
    expect(
      screen.getByRole("button", { name: /transfer/i }),
    ).toBeDisabled();
  });

  it("excludes the current org from the destination list", async () => {
    renderSection();
    const user = userEvent.setup();
    await user.click(
      screen.getByRole("combobox", { name: /destination organization/i }),
    );
    // beta + gamma are offered; acme (the current org) is not.
    expect(screen.getByRole("option", { name: "beta" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "gamma" })).toBeInTheDocument();
    expect(
      screen.queryByRole("option", { name: "acme" }),
    ).not.toBeInTheDocument();
  });

  it("transfers via the mutation and navigates on success", async () => {
    renderSection();
    const user = userEvent.setup();

    await user.click(
      screen.getByRole("combobox", { name: /destination organization/i }),
    );
    await user.click(screen.getByRole("option", { name: "beta" }));

    // Open the confirm dialog, then confirm.
    await user.click(screen.getByRole("button", { name: /transfer…/i }));
    await user.click(
      screen.getByRole("button", { name: /transfer repository/i }),
    );

    expect(mockMutate).toHaveBeenCalledWith({
      org: "acme",
      repo: "api",
      dest_org: "beta",
    });
    expect(mockNavigate).toHaveBeenCalledWith({
      to: "/repositories/$org/$repo",
      params: { org: "beta", repo: "api" },
    });
  });
});

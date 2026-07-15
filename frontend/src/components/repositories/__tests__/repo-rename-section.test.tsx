import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RepoRenameSection } from "../repo-rename-section";

// Tests for the General-section Rename editor (repo rename feature, PR C).
// Behaviours:
//   - Renders the repo's current name in the input.
//   - The Rename button stays disabled until the draft is a valid, changed name.
//   - An invalid name (uppercase / spaces) surfaces an error and blocks submit.
//   - Confirming the dialog fires the mutation with { org, repo, new_name } and,
//     on success, navigates to the repo's new URL.
//
// The repositories API, router navigation, and toast are mocked so no HTTP or
// routing is touched — same pattern as the sibling repo-description-section
// tests.

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
    useRenameRepository: () => ({
      mutateAsync: mockMutate,
      mutate: mockMutate,
      isPending: mockPending,
      error: null,
      reset: vi.fn(),
    }),
  };
});

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
      <RepoRenameSection org="acme" repo="api" />
    </QueryClientProvider>,
  );
}

describe("RepoRenameSection", () => {
  beforeEach(() => {
    mockMutate.mockReset();
    mockMutate.mockResolvedValue({ roles_rewritten: 3 });
    mockNavigate.mockReset();
    mockPending = false;
  });

  it("renders the current repo name in the input", () => {
    renderSection();
    expect(screen.getByRole("textbox")).toHaveValue("api");
  });

  it("keeps Rename disabled until the name is changed", async () => {
    renderSection();
    const user = userEvent.setup();
    expect(screen.getByRole("button", { name: /rename/i })).toBeDisabled();
    await user.type(screen.getByRole("textbox"), "-v2");
    expect(screen.getByRole("button", { name: /rename/i })).toBeEnabled();
  });

  it("blocks an invalid name and shows an error", async () => {
    renderSection();
    const user = userEvent.setup();
    const box = screen.getByRole("textbox");
    await user.clear(box);
    await user.type(box, "Bad Name");
    expect(screen.getByText(/lowercase letters, digits/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /rename/i })).toBeDisabled();
  });

  it("renames via the mutation and navigates on success", async () => {
    renderSection();
    const user = userEvent.setup();
    const box = screen.getByRole("textbox");
    await user.clear(box);
    await user.type(box, "api-v2");

    // Open the confirm dialog, then confirm.
    await user.click(screen.getByRole("button", { name: /rename…/i }));
    await user.click(
      screen.getByRole("button", { name: /rename repository/i }),
    );

    expect(mockMutate).toHaveBeenCalledWith({
      org: "acme",
      repo: "api",
      new_name: "api-v2",
    });
    expect(mockNavigate).toHaveBeenCalledWith({
      to: "/repositories/$org/$repo",
      params: { org: "acme", repo: "api-v2" },
    });
  });
});

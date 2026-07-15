import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RepoVisibilitySection } from "../repo-visibility-section";

// Tests for the General-section visibility toggle (Tier 2 #2). Behaviours:
//   - Private repo → switch off, "Private" badge.
//   - Flipping on → mutation sends is_public: true.
//   - Public repo → switch on; flipping off → mutation sends is_public: false.

const mockMutate = vi.fn();
let mockPending = false;
let mockRepo: Record<string, unknown> | undefined;

vi.mock("@/lib/api/repositories", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api/repositories")>(
      "@/lib/api/repositories",
    );
  return {
    ...actual,
    useRepository: () => ({
      data: mockRepo,
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    }),
    useUpdateRepository: () => ({
      mutateAsync: mockMutate,
      mutate: mockMutate,
      isPending: mockPending,
      error: null,
      reset: vi.fn(),
    }),
  };
});

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

function renderSection() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <RepoVisibilitySection org="acme" repo="api" />
    </QueryClientProvider>,
  );
}

describe("RepoVisibilitySection", () => {
  beforeEach(() => {
    mockMutate.mockReset();
    mockMutate.mockResolvedValue({});
    mockPending = false;
    mockRepo = { repo_id: "r1", org: "acme", name: "api", is_public: false };
  });

  it("shows the Private badge and an off switch for a private repo", () => {
    renderSection();
    expect(screen.getByText(/^private$/i)).toBeInTheDocument();
    expect(screen.getByRole("switch")).not.toBeChecked();
  });

  it("flips a private repo public → sends is_public: true", async () => {
    renderSection();
    const user = userEvent.setup();
    await user.click(screen.getByRole("switch"));
    expect(mockMutate).toHaveBeenCalledWith({
      org: "acme",
      repo: "api",
      is_public: true,
    });
  });

  it("flips a public repo private → sends is_public: false", async () => {
    mockRepo = { ...mockRepo, is_public: true };
    renderSection();
    const user = userEvent.setup();
    expect(screen.getByRole("switch")).toBeChecked();
    await user.click(screen.getByRole("switch"));
    expect(mockMutate).toHaveBeenCalledWith({
      org: "acme",
      repo: "api",
      is_public: false,
    });
  });
});

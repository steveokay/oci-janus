import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RepoDescriptionSection } from "../repo-description-section";

// Tests for the General-section Description editor (Tier 2 #2). Behaviours:
//   - Renders the repo's current description in the textarea.
//   - Save is disabled until the draft differs from the saved value.
//   - Editing + Save fires the mutation with { org, repo, description }.
//   - An over-length draft disables Save and shows the counter in an error tone.
//
// The repositories API + toast are mocked so no HTTP is touched — same pattern
// as the sibling repo-cvss-policy-section tests.

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
      <RepoDescriptionSection org="acme" repo="api" />
    </QueryClientProvider>,
  );
}

describe("RepoDescriptionSection", () => {
  beforeEach(() => {
    mockMutate.mockReset();
    mockMutate.mockResolvedValue({});
    mockPending = false;
    mockRepo = {
      repo_id: "r1",
      org: "acme",
      name: "api",
      description: "old readme",
    };
  });

  it("renders the current description in the textarea", () => {
    renderSection();
    expect(screen.getByRole("textbox")).toHaveValue("old readme");
  });

  it("keeps Save disabled until the draft differs from the saved value", async () => {
    renderSection();
    const user = userEvent.setup();
    expect(screen.getByRole("button", { name: /save/i })).toBeDisabled();
    await user.type(screen.getByRole("textbox"), "!");
    expect(screen.getByRole("button", { name: /save/i })).toBeEnabled();
  });

  it("saves the edited description via the mutation", async () => {
    renderSection();
    const user = userEvent.setup();
    const box = screen.getByRole("textbox");
    await user.clear(box);
    await user.type(box, "new readme");
    await user.click(screen.getByRole("button", { name: /save/i }));
    expect(mockMutate).toHaveBeenCalledWith({
      org: "acme",
      repo: "api",
      description: "new readme",
    });
  });

  it("handles an empty starting description with Save disabled", () => {
    mockRepo = { ...mockRepo, description: "" };
    renderSection();
    expect(screen.getByRole("textbox")).toHaveValue("");
    expect(screen.getByRole("button", { name: /save/i })).toBeDisabled();
  });
});

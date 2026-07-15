import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RepoQuotaSection } from "../repo-quota-section";

// Tests for the General-section storage-quota override (Tier 2 #2). Behaviours:
//   - Prefills the input from the repo's current quota (10 GiB → "10").
//   - Save disabled until the draft differs from the current quota.
//   - Editing to 20 GB + Save fires the mutation with the byte value.
//   - Zero / empty disables Save.

const GIB = 1024 ** 3;
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
      <RepoQuotaSection org="acme" repo="api" />
    </QueryClientProvider>,
  );
}

describe("RepoQuotaSection", () => {
  beforeEach(() => {
    mockMutate.mockReset();
    mockMutate.mockResolvedValue({});
    mockPending = false;
    mockRepo = {
      repo_id: "r1",
      org: "acme",
      name: "api",
      storage_quota_bytes: 10 * GIB,
      storage_used_bytes: 1 * GIB,
    };
  });

  it("prefills the current quota in GB", () => {
    renderSection();
    expect(screen.getByRole("spinbutton")).toHaveValue(10);
  });

  it("keeps Save disabled until the quota changes", async () => {
    renderSection();
    const user = userEvent.setup();
    expect(screen.getByRole("button", { name: /save/i })).toBeDisabled();
    const input = screen.getByRole("spinbutton");
    await user.clear(input);
    await user.type(input, "20");
    expect(screen.getByRole("button", { name: /save/i })).toBeEnabled();
  });

  it("saves the new quota as bytes via the mutation", async () => {
    renderSection();
    const user = userEvent.setup();
    const input = screen.getByRole("spinbutton");
    await user.clear(input);
    await user.type(input, "20");
    await user.click(screen.getByRole("button", { name: /save/i }));
    expect(mockMutate).toHaveBeenCalledWith({
      org: "acme",
      repo: "api",
      storage_quota_bytes: 20 * GIB,
    });
  });

  it("disables Save for an empty / zero value", async () => {
    renderSection();
    const user = userEvent.setup();
    const input = screen.getByRole("spinbutton");
    await user.clear(input);
    expect(screen.getByRole("button", { name: /save/i })).toBeDisabled();
  });
});

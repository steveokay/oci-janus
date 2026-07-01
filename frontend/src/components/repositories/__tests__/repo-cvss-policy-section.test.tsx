import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RepoCVSSPolicySection } from "../repo-cvss-policy-section";

// Tests for the FUT-021 CVSS admission section. Load-bearing behaviours:
//   - "Open" badge when the threshold is null on the repo row.
//   - Flip toggle → mutation sends max_cvss_score: 69 (default).
//   - Turn OFF while enabled → mutation sends max_cvss_score: null.
//   - Preset click updates the input; Save fires the mutation with the value.
//   - Out-of-range value shows inline validation and skips the mutation.
//
// The repositories API is mocked so no HTTP is touched; the toast is
// stubbed for the same reason as the sibling PromoteTagDialog tests.

const mockMutate = vi.fn();
let mockPending = false;
let mockRepo: Record<string, unknown> | undefined = {
  repo_id: "r1",
  org: "acme",
  name: "api",
  is_public: true,
  storage_used_bytes: 0,
  storage_quota_bytes: 0,
  created_at: "2026-01-01T00:00:00Z",
  description: "",
  max_cvss_score: null,
};

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
      <RepoCVSSPolicySection org="acme" repo="api" />
    </QueryClientProvider>,
  );
}

describe("RepoCVSSPolicySection", () => {
  beforeEach(() => {
    mockMutate.mockReset();
    mockMutate.mockResolvedValue({});
    mockPending = false;
    mockRepo = {
      repo_id: "r1",
      org: "acme",
      name: "api",
      is_public: true,
      storage_used_bytes: 0,
      storage_quota_bytes: 0,
      created_at: "2026-01-01T00:00:00Z",
      description: "",
      max_cvss_score: null,
    };
  });

  it("renders the Open badge when no threshold is configured", () => {
    renderSection();
    // The badge text is the specific "Open" label without a trailing colon;
    // /open/i alone matches "Fails OPEN…" prose too, so anchor to the badge.
    expect(screen.getByText(/^open$/i)).toBeInTheDocument();
    // The Save button only appears when the switch is on. Use the button
    // role to sidestep the number-input label ambiguity across shadcn variants.
    expect(
      screen.queryByRole("button", { name: /^save$/i }),
    ).not.toBeInTheDocument();
  });

  it("flips the switch on → sends the default threshold to the BFF", async () => {
    renderSection();
    const user = userEvent.setup();
    await user.click(screen.getByRole("switch"));
    expect(mockMutate).toHaveBeenCalledWith({
      org: "acme",
      repo: "api",
      max_cvss_score: 69,
    });
  });

  it("flips the switch off while enabled → sends explicit null to clear", async () => {
    mockRepo = { ...mockRepo, max_cvss_score: 70 };
    renderSection();
    const user = userEvent.setup();
    // Switch is now on because max_cvss_score is set on the repo row.
    await user.click(screen.getByRole("switch"));
    expect(mockMutate).toHaveBeenCalledWith({
      org: "acme",
      repo: "api",
      max_cvss_score: null,
    });
  });

  it("clicking a preset updates the number input", async () => {
    mockRepo = { ...mockRepo, max_cvss_score: 70 };
    renderSection();
    const user = userEvent.setup();
    // "CRITICAL only" preset = 89. Click it and observe the input updates.
    await user.click(screen.getByRole("button", { name: /critical only/i }));
    // Use role=spinbutton (native number input) to disambiguate — the
    // component sets id="cvss-threshold" but React Testing Library's
    // getByLabelText matches both the label and the descriptive text.
    const input = screen.getByRole("spinbutton") as HTMLInputElement;
    expect(input.value).toBe("89");
  });

  it("saves the draft threshold via the mutation on Save", async () => {
    mockRepo = { ...mockRepo, max_cvss_score: 70 };
    renderSection();
    const user = userEvent.setup();
    // Change via preset → Save.
    await user.click(screen.getByRole("button", { name: /any finding/i }));
    await user.click(screen.getByRole("button", { name: /save/i }));
    expect(mockMutate).toHaveBeenCalledWith({
      org: "acme",
      repo: "api",
      max_cvss_score: 0,
    });
  });

  it("rejects out-of-range input with inline validation and no mutation", async () => {
    mockRepo = { ...mockRepo, max_cvss_score: 70 };
    renderSection();
    const user = userEvent.setup();
    // Use role=spinbutton (native number input) to disambiguate — the
    // component sets id="cvss-threshold" but React Testing Library's
    // getByLabelText matches both the label and the descriptive text.
    const input = screen.getByRole("spinbutton") as HTMLInputElement;
    await user.clear(input);
    await user.type(input, "150");
    await user.click(screen.getByRole("button", { name: /save/i }));
    expect(
      screen.getByText(/threshold must be an integer 0-100/i),
    ).toBeInTheDocument();
    expect(mockMutate).not.toHaveBeenCalled();
  });
});

import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { PRRegistryPanel } from "../pr-registry-panel";
import type { PRRegistryConfig } from "@/lib/api/pr-registry";

// Tests for the FUT-023 PRRegistryPanel. The API hooks + the admin gate are
// mocked so no network is involved. sonner is stubbed to keep toasts quiet.

// Mutable holders reset per test.
let mockIsAdmin = true;
let mockData: PRRegistryConfig | undefined;
let mockIsLoading = false;
let mockIsError = false;
const mockUpdateMutate = vi.fn();

vi.mock("@/lib/api/abilities", () => ({
  useIsGlobalAdmin: () => mockIsAdmin,
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

// Repositories feed the promote-target dropdown — return an empty page so the
// Select renders with just the "None" option (no network).
vi.mock("@/lib/api/repositories", () => ({
  useRepositories: () => ({ data: { pages: [] } }),
}));

vi.mock("@/lib/api/pr-registry", () => ({
  usePRRegistryConfig: () => ({
    data: mockData,
    isLoading: mockIsLoading,
    isError: mockIsError,
    refetch: vi.fn(),
  }),
  useUpdatePRRegistryConfig: () => ({
    mutate: mockUpdateMutate,
    isPending: false,
  }),
}));

// fixtureConfig — a PRRegistryConfig with sensible defaults; tests override
// only the fields they care about.
function fixtureConfig(
  overrides: Partial<PRRegistryConfig> = {},
): PRRegistryConfig {
  return {
    enabled: false,
    has_secret: false,
    promote_target_org: "",
    webhook_url: "https://reg.example.com/webhooks/scm/github/pr",
    ...overrides,
  };
}

function renderPanel() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <PRRegistryPanel />
    </QueryClientProvider>,
  );
}

describe("PRRegistryPanel", () => {
  beforeEach(() => {
    mockIsAdmin = true;
    mockData = fixtureConfig();
    mockIsLoading = false;
    mockIsError = false;
    mockUpdateMutate.mockReset();
  });

  it("renders nothing for non-admins", () => {
    mockIsAdmin = false;
    const { container } = renderPanel();
    expect(container).toBeEmptyDOMElement();
  });

  it("renders the webhook URL + secret fields for admins", async () => {
    renderPanel();
    await waitFor(() =>
      expect(screen.getByLabelText(/webhook receiver url/i)).toHaveValue(
        "https://reg.example.com/webhooks/scm/github/pr",
      ),
    );
    expect(screen.getByLabelText(/signing secret/i)).toBeInTheDocument();
  });

  it("hints the secret is already configured via a placeholder", async () => {
    mockData = fixtureConfig({ has_secret: true });
    renderPanel();
    const secret = await screen.findByLabelText(/signing secret/i);
    // Write-only: value stays empty, only the placeholder signals "set".
    expect(secret).toHaveValue("");
    expect(secret).toHaveAttribute("placeholder", "•••• configured");
  });

  it("Save sends an empty secret when the field is left blank", async () => {
    mockData = fixtureConfig({ promote_target_org: "prod", enabled: true });
    const user = userEvent.setup();
    renderPanel();
    await waitFor(() => screen.getByLabelText(/webhook receiver url/i));

    await user.click(screen.getByRole("button", { name: /^save$/i }));

    expect(mockUpdateMutate).toHaveBeenCalledTimes(1);
    const body = mockUpdateMutate.mock.calls[0][0];
    expect(body.webhook_secret).toBe("");
    // The stored target + enable state round-trip through the form unchanged.
    expect(body.promote_target_org).toBe("prod");
    expect(body.enabled).toBe(true);
  });

  it("Save forwards a newly-typed secret", async () => {
    const user = userEvent.setup();
    renderPanel();
    const secret = await screen.findByLabelText(/signing secret/i);
    await user.type(secret, "hunter2");
    await user.click(screen.getByRole("button", { name: /^save$/i }));

    const body = mockUpdateMutate.mock.calls[0][0];
    expect(body.webhook_secret).toBe("hunter2");
  });
});

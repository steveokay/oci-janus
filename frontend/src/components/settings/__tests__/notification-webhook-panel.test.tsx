import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { NotificationWebhookPanel } from "../notification-webhook-panel";
import type { NotificationWebhookConfig } from "@/lib/api/notification-webhook";

// Tests for the FUT-019 Phase 3 NotificationWebhookPanel. The API hooks + the
// admin gate are mocked so no network is involved and the panel always mounts
// (admin=true). sonner is stubbed to keep toast side effects quiet.

// Mutable holders reset per test.
let mockData: NotificationWebhookConfig | undefined;
let mockIsLoading = false;
let mockIsError = false;
const mockUpdateMutate = vi.fn();
const mockTestMutate = vi.fn();
let mockTestData: { ok: boolean; error: string } | undefined;

vi.mock("@/lib/api/abilities", () => ({
  useIsGlobalAdmin: () => true,
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

vi.mock("@/lib/api/notification-webhook", () => ({
  useNotificationWebhook: () => ({
    data: mockData,
    isLoading: mockIsLoading,
    isError: mockIsError,
    refetch: vi.fn(),
  }),
  useUpdateNotificationWebhook: () => ({
    mutate: mockUpdateMutate,
    isPending: false,
  }),
  useSendTestNotificationWebhook: () => ({
    mutate: mockTestMutate,
    isPending: false,
    data: mockTestData,
  }),
}));

// fixtureConfig — a NotificationWebhookConfig with sensible defaults; tests
// override only the fields they care about.
function fixtureConfig(
  overrides: Partial<NotificationWebhookConfig> = {},
): NotificationWebhookConfig {
  return {
    url: "https://hooks.example.com/abc",
    enabled: true,
    has_secret: false,
    enabled_categories: [],
    ...overrides,
  };
}

function renderPanel() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={qc}>
      <NotificationWebhookPanel />
    </QueryClientProvider>,
  );
}

describe("NotificationWebhookPanel", () => {
  beforeEach(() => {
    mockData = fixtureConfig();
    mockIsLoading = false;
    mockIsError = false;
    mockTestData = undefined;
    mockUpdateMutate.mockReset();
    mockTestMutate.mockReset();
  });

  it("renders the webhook URL field for admins", async () => {
    renderPanel();
    await waitFor(() =>
      expect(screen.getByLabelText(/webhook url/i)).toHaveValue(
        "https://hooks.example.com/abc",
      ),
    );
    expect(screen.getByLabelText(/signing secret/i)).toBeInTheDocument();
  });

  it("renders a 'Send test' button for admins", async () => {
    renderPanel();
    await waitFor(() => screen.getByLabelText(/webhook url/i));
    expect(
      screen.getByRole("button", { name: /send test/i }),
    ).toBeInTheDocument();
  });

  it("Save preserves enabled_categories + sends an empty secret when left blank", async () => {
    mockData = fixtureConfig({ enabled_categories: ["scan.completed"] });
    const user = userEvent.setup();
    renderPanel();
    await waitFor(() => screen.getByLabelText(/webhook url/i));

    await user.click(screen.getByRole("button", { name: /^save$/i }));

    expect(mockUpdateMutate).toHaveBeenCalledTimes(1);
    const body = mockUpdateMutate.mock.calls[0][0];
    expect(body.secret).toBe("");
    expect(body.enabled_categories).toEqual(["scan.completed"]);
  });

  it("shows the returned result after clicking 'Send test'", async () => {
    const user = userEvent.setup();
    renderPanel();
    await waitFor(() => screen.getByLabelText(/webhook url/i));

    await user.click(screen.getByRole("button", { name: /send test/i }));
    expect(mockTestMutate).toHaveBeenCalledTimes(1);

    // Simulate the mutation resolving with a success payload + re-render.
    mockTestData = { ok: true, error: "" };
    renderPanel();
    expect(
      await screen.findByText(/test webhook sent successfully/i),
    ).toBeInTheDocument();
  });
});

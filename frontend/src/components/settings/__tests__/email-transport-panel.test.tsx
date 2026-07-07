import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { EmailTransportPanel } from "../email-transport-panel";
import type { EmailTransportConfig } from "@/lib/api/email-transport";

// Tests for the FUT-019 Phase 3 EmailTransportPanel. The API hooks + the
// admin gate are mocked so no network is involved and the panel always
// mounts (admin=true). sonner is stubbed to keep toast side effects quiet.

// Mutable holders reset per test.
let mockData: EmailTransportConfig | undefined;
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

vi.mock("@/lib/api/email-transport", () => ({
  useEmailTransport: () => ({
    data: mockData,
    isLoading: mockIsLoading,
    isError: mockIsError,
    refetch: vi.fn(),
  }),
  useUpdateEmailTransport: () => ({
    mutate: mockUpdateMutate,
    isPending: false,
  }),
  useSendTestEmail: () => ({
    mutate: mockTestMutate,
    isPending: false,
    data: mockTestData,
  }),
}));

// fixtureConfig — an EmailTransportConfig with sensible defaults; tests
// override only the fields they care about.
function fixtureConfig(
  overrides: Partial<EmailTransportConfig> = {},
): EmailTransportConfig {
  return {
    provider: "resend",
    enabled: true,
    from_address: "noreply@example.com",
    from_name: "Registry",
    smtp_host: "",
    smtp_port: 0,
    smtp_username: "",
    smtp_tls_mode: "starttls",
    has_resend_key: false,
    has_smtp_password: false,
    ...overrides,
  };
}

function renderPanel() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={qc}>
      <EmailTransportPanel />
    </QueryClientProvider>,
  );
}

describe("EmailTransportPanel", () => {
  beforeEach(() => {
    mockData = fixtureConfig();
    mockIsLoading = false;
    mockIsError = false;
    mockTestData = undefined;
    mockUpdateMutate.mockReset();
    mockTestMutate.mockReset();
  });

  it("renders the resend provider fields", async () => {
    renderPanel();
    // Provider select seeded to resend + the Resend API key field visible.
    await waitFor(() =>
      expect(screen.getByLabelText("Provider")).toHaveValue("resend"),
    );
    expect(screen.getByLabelText(/resend api key/i)).toBeInTheDocument();
    // SMTP host field is not rendered while provider = resend.
    expect(screen.queryByLabelText(/smtp host/i)).not.toBeInTheDocument();
  });

  it("switches to SMTP + Gmail presets when 'Use Gmail' is clicked", async () => {
    const user = userEvent.setup();
    renderPanel();
    await waitFor(() => screen.getByLabelText("Provider"));

    await user.click(screen.getByRole("button", { name: /use gmail/i }));

    expect(screen.getByLabelText("Provider")).toHaveValue("smtp");
    expect(screen.getByLabelText(/smtp host/i)).toHaveValue("smtp.gmail.com");
    expect(screen.getByLabelText(/port/i)).toHaveValue(587);
    expect(screen.getByLabelText(/tls mode/i)).toHaveValue("starttls");
  });

  it("Save sends an empty resend_api_key when the key field is left blank", async () => {
    const user = userEvent.setup();
    renderPanel();
    await waitFor(() => screen.getByLabelText("Provider"));

    await user.click(screen.getByRole("button", { name: /^save$/i }));

    expect(mockUpdateMutate).toHaveBeenCalledTimes(1);
    const body = mockUpdateMutate.mock.calls[0][0];
    expect(body.resend_api_key).toBe("");
    expect(body.smtp_password).toBe("");
    expect(body.provider).toBe("resend");
  });

  it("shows the returned result after clicking 'Send test email'", async () => {
    const user = userEvent.setup();
    renderPanel();
    await waitFor(() => screen.getByLabelText("Provider"));

    await user.click(screen.getByRole("button", { name: /send test email/i }));
    expect(mockTestMutate).toHaveBeenCalledTimes(1);

    // Simulate the mutation resolving with a success payload + re-render.
    mockTestData = { ok: true, error: "" };
    renderPanel();
    expect(
      await screen.findByText(/test email sent successfully/i),
    ).toBeInTheDocument();
  });
});

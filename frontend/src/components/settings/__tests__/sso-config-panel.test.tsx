import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { SSOConfigPanel } from "../sso-config-panel";
import type { AdminProvider } from "@/lib/api/sso-config";

// Tests for the FE-API-034 SSOConfigPanel. The API hooks + the admin gate are
// mocked so no network is involved. sonner is stubbed to keep toast side
// effects quiet.

// Mutable holders reset per test.
let mockProviders: AdminProvider[] | undefined;
let mockIsLoading = false;
let mockIsError = false;
let mockIsAdmin = true;
const mockUpsertMutate = vi.fn();
const mockDeleteMutate = vi.fn();

vi.mock("@/lib/api/abilities", () => ({
  useIsGlobalAdmin: () => mockIsAdmin,
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

vi.mock("@/lib/api/sso-config", () => ({
  useSSOProviders: () => ({
    data: mockProviders,
    isLoading: mockIsLoading,
    isError: mockIsError,
    refetch: vi.fn(),
  }),
  useUpsertSSOProvider: () => ({
    mutate: mockUpsertMutate,
    isPending: false,
  }),
  useDeleteSSOProvider: () => ({
    mutate: mockDeleteMutate,
    isPending: false,
  }),
}));

// fixtureProvider — an AdminProvider with sensible defaults; tests override
// only the fields they care about.
function fixtureProvider(
  overrides: Partial<AdminProvider> = {},
): AdminProvider {
  return {
    id: "google",
    kind: "oauth_google",
    display_name: "Google",
    enabled: true,
    oauth_client_id: "client-abc",
    oauth_issuer_url: "",
    oauth_scopes: ["openid", "email"],
    has_secret: true,
    auto_provision: true,
    created_at: "2026-07-13T00:00:00Z",
    updated_at: "2026-07-13T00:00:00Z",
    ...overrides,
  };
}

function renderPanel() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={qc}>
      <SSOConfigPanel />
    </QueryClientProvider>,
  );
}

describe("SSOConfigPanel", () => {
  beforeEach(() => {
    mockProviders = [fixtureProvider()];
    mockIsLoading = false;
    mockIsError = false;
    mockIsAdmin = true;
    mockUpsertMutate.mockReset();
    mockDeleteMutate.mockReset();
  });

  it("renders configured providers from the hook", async () => {
    mockProviders = [
      fixtureProvider({ id: "google", display_name: "Google" }),
      fixtureProvider({
        id: "github",
        kind: "oauth_github",
        display_name: "GitHub Enterprise",
        has_secret: false,
      }),
    ];
    renderPanel();
    await waitFor(() => screen.getByText("Google"));
    expect(screen.getByText("Google")).toBeInTheDocument();
    expect(screen.getByText("GitHub Enterprise")).toBeInTheDocument();
  });

  it("renders nothing for non-admins", () => {
    mockIsAdmin = false;
    const { container } = renderPanel();
    expect(container).toBeEmptyDOMElement();
  });

  it("does not offer SAML as a kind option", async () => {
    const user = userEvent.setup();
    renderPanel();
    await waitFor(() => screen.getByText("Google"));

    // Open the add form so the kind Select is mounted.
    await user.click(screen.getByRole("button", { name: /add provider/i }));

    const kindSelect = screen.getByLabelText(/provider kind/i);
    expect(within(kindSelect).queryByText(/saml/i)).not.toBeInTheDocument();
    // And the four OAuth kinds are present as options.
    expect(within(kindSelect).getByRole("option", { name: /google/i }))
      .toBeInTheDocument();
    expect(within(kindSelect).getByRole("option", { name: /github/i }))
      .toBeInTheDocument();
    expect(within(kindSelect).getByRole("option", { name: /microsoft/i }))
      .toBeInTheDocument();
    expect(within(kindSelect).getByRole("option", { name: /generic/i }))
      .toBeInTheDocument();
  });

  it("submits the form with an empty client_secret when editing without retyping the secret", async () => {
    mockProviders = [
      fixtureProvider({
        id: "google",
        kind: "oauth_google",
        display_name: "Google",
        oauth_client_id: "client-abc",
        has_secret: true,
      }),
    ];
    const user = userEvent.setup();
    renderPanel();
    await waitFor(() => screen.getByText("Google"));

    await user.click(screen.getByRole("button", { name: /^edit$/i }));
    // Save without touching the secret input.
    await user.click(screen.getByRole("button", { name: /^save$/i }));

    expect(mockUpsertMutate).toHaveBeenCalledTimes(1);
    const arg = mockUpsertMutate.mock.calls[0][0];
    expect(arg.id).toBe("google");
    expect(arg.body.client_secret).toBe("");
    expect(arg.body.kind).toBe("oauth_google");
    expect(arg.body.oauth_client_id).toBe("client-abc");
  });

  it("calls the delete mutation when a provider is deleted", async () => {
    mockProviders = [fixtureProvider({ id: "google", display_name: "Google" })];
    const user = userEvent.setup();
    renderPanel();
    await waitFor(() => screen.getByText("Google"));

    await user.click(screen.getByRole("button", { name: /^delete$/i }));

    // Confirm in the destructive dialog (severity="low" → single confirm).
    const confirmBtn = await screen.findByRole("button", {
      name: /remove provider|delete provider|confirm|remove|delete/i,
    });
    await user.click(confirmBtn);

    await waitFor(() =>
      expect(mockDeleteMutate).toHaveBeenCalledWith(
        "google",
        expect.anything(),
      ),
    );
  });
});

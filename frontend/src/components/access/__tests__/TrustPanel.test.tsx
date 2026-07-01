import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { TrustPanel } from "../TrustPanel";
import type { OIDCTrust } from "@/lib/api/oidc-trust";

// Tests for the live TrustPanel (FUT-001 Task 17). Mocks the hooks so
// no network is involved.

// Mutable holders per test.
let mockTrusts: OIDCTrust[] = [];
let mockIsLoading = false;
let mockIsError = false;
const mockDelete = vi.fn();

vi.mock("@/lib/api/oidc-trust", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api/oidc-trust")>(
    "@/lib/api/oidc-trust",
  );
  return {
    ...actual,
    useOIDCTrusts: () => ({
      data: mockTrusts,
      isLoading: mockIsLoading,
      isError: mockIsError,
    }),
    useDeleteOIDCTrust: () => ({
      mutate: mockDelete,
      mutateAsync: mockDelete,
      isPending: false,
    }),
    useCreateOIDCTrust: () => ({
      mutateAsync: vi.fn(),
      mutate: vi.fn(),
      isPending: false,
      error: null,
      reset: vi.fn(),
    }),
  };
});

// Service accounts feed the CreateOIDCTrustDialog's picker. The panel
// mounts the dialog even when closed, so the hook is called on render.
vi.mock("@/lib/api/service-accounts", async () => {
  const actual = await vi.importActual<
    typeof import("@/lib/api/service-accounts")
  >("@/lib/api/service-accounts");
  return {
    ...actual,
    useServiceAccounts: () => ({
      data: [],
      isLoading: false,
      isError: false,
    }),
  };
});

function fixtureTrust(overrides: Partial<OIDCTrust> = {}): OIDCTrust {
  return {
    id: "trust-1",
    tenant_id: "tenant-1",
    service_account_id: "sa-1",
    display_name: "GHA prod",
    issuer_url: "https://token.actions.githubusercontent.com",
    audience: "registry",
    subject_pattern: "repo:steveokay/oci-janus:ref:refs/heads/main",
    jwks_cache_ttl_seconds: 3600,
    created_at: "2026-06-30T00:00:00Z",
    updated_at: "2026-06-30T00:00:00Z",
    last_used_at: null,
    ...overrides,
  };
}

function renderPanel() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={qc}>
      <TrustPanel />
    </QueryClientProvider>,
  );
}

describe("TrustPanel", () => {
  beforeEach(() => {
    mockTrusts = [];
    mockIsLoading = false;
    mockIsError = false;
    mockDelete.mockReset();
  });

  it("renders the heading and does NOT render the amber preview banner", () => {
    renderPanel();
    expect(
      screen.getByRole("heading", { name: /federated trust/i }),
    ).toBeInTheDocument();
    // Preview kicker / banner text is gone now that the surface is live.
    expect(
      screen.queryByText(/Sprint 11.*FUT-001/i),
    ).not.toBeInTheDocument();
  });

  it("renders a loading state while data fetches", () => {
    mockIsLoading = true;
    renderPanel();
    expect(screen.getByRole("status")).toBeInTheDocument();
  });

  it("renders an empty state when no trusts exist", () => {
    mockTrusts = [];
    renderPanel();
    expect(
      screen.getByText(/no federated trusts yet/i),
    ).toBeInTheDocument();
  });

  it("renders a card per trust with display name + last-verified text", () => {
    mockTrusts = [
      fixtureTrust({ id: "t-1", display_name: "GHA prod", last_used_at: null }),
      fixtureTrust({
        id: "t-2",
        display_name: "GitLab main",
        last_used_at: "2026-06-30T12:00:00Z",
      }),
    ];
    renderPanel();
    expect(screen.getByText("GHA prod")).toBeInTheDocument();
    expect(screen.getByText("GitLab main")).toBeInTheDocument();
    // Never-used shows the sentinel string.
    expect(screen.getByText(/last verified: never/i)).toBeInTheDocument();
  });

  it("invokes the delete mutation when the delete action is chosen", async () => {
    const user = userEvent.setup();
    mockTrusts = [fixtureTrust({ id: "t-42" })];
    renderPanel();

    // Kebab menu is an aria-label'd button; label includes the display
    // name so multiple cards can coexist.
    await user.click(
      screen.getByRole("button", { name: /trust actions/i }),
    );
    await user.click(screen.getByRole("menuitem", { name: /delete/i }));

    await waitFor(() => expect(mockDelete).toHaveBeenCalledWith("t-42"));
  });
});

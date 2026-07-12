import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SignManifestDialog } from "../sign-manifest-dialog";
import type { ServiceAccount } from "@/lib/api/service-accounts";

// FUT-009 — SignManifestDialog identity picker.
//
// We mock useSignManifest (capture the payload) + useServiceAccounts (feed the
// Select). We assert:
//   - the SA Select is the default and is populated from useServiceAccounts,
//   - picking an SA option submits { service_account_id: <shadow_user_id> },
//   - the empty-state hint appears when the tenant has no service accounts.

const mutateAsync = vi.fn();

vi.mock("@/lib/api/signature", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api/signature")>(
      "@/lib/api/signature",
    );
  return {
    ...actual,
    useSignManifest: () => ({ mutateAsync, isPending: false }),
  };
});

let serviceAccounts: ServiceAccount[];
vi.mock("@/lib/api/service-accounts", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api/service-accounts")>(
      "@/lib/api/service-accounts",
    );
  return {
    ...actual,
    useServiceAccounts: () => ({ data: serviceAccounts, isLoading: false }),
  };
});

// sonner toast is a side effect we don't assert on here.
vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

const SA_SHADOW = "11111111-1111-1111-1111-111111111111";

function makeSA(overrides: Partial<ServiceAccount> = {}): ServiceAccount {
  return {
    id: "sa-1",
    tenant_id: "t-1",
    name: "ci-prod-bot",
    description: "",
    allowed_scopes: [],
    shadow_user_id: SA_SHADOW,
    created_at: "2026-07-11T00:00:00Z",
    active_key_count: 1,
    ...overrides,
  };
}

beforeEach(() => {
  mutateAsync.mockReset();
  mutateAsync.mockResolvedValue({ signer_id: "ci-prod-bot" });
  serviceAccounts = [makeSA()];
});

describe("SignManifestDialog (FUT-009)", () => {
  it("defaults to the service-account Select populated from useServiceAccounts", () => {
    render(
      <SignManifestDialog
        open
        onOpenChange={() => {}}
        org="acme"
        repo="api"
        tag="v1.0"
      />,
    );
    // The Select trigger renders with the SA placeholder — the default mode.
    expect(
      screen.getByRole("combobox", { name: /signing identity/i }),
    ).toBeInTheDocument();
    expect(screen.getByText("Select a service account")).toBeInTheDocument();
  });

  it("submits service_account_id (the SA shadow_user_id) when an SA is picked", async () => {
    const user = userEvent.setup();
    const onOpenChange = vi.fn();
    render(
      <SignManifestDialog
        open
        onOpenChange={onOpenChange}
        org="acme"
        repo="api"
        tag="v1.0"
      />,
    );

    // Open the Radix Select and choose the SA option.
    await user.click(
      screen.getByRole("combobox", { name: /signing identity/i }),
    );
    await user.click(await screen.findByRole("option", { name: "ci-prod-bot" }));

    // Submit the form.
    await user.click(screen.getByRole("button", { name: /^sign$/i }));

    expect(mutateAsync).toHaveBeenCalledTimes(1);
    expect(mutateAsync).toHaveBeenCalledWith({
      org: "acme",
      repo: "api",
      tag: "v1.0",
      service_account_id: SA_SHADOW,
    });
  });

  it("shows the empty-state hint when the tenant has no service accounts", () => {
    serviceAccounts = [];
    render(
      <SignManifestDialog
        open
        onOpenChange={() => {}}
        org="acme"
        repo="api"
        tag="v1.0"
      />,
    );
    expect(screen.getByText(/no service accounts yet/i)).toBeInTheDocument();
  });
});

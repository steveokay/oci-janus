import * as React from "react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { CreateOIDCTrustDialog } from "../CreateOIDCTrustDialog";
import type { ServiceAccount } from "@/lib/api/service-accounts";

// Tests for the CreateOIDCTrustDialog (FUT-001 Task 16). Mocks the
// hooks module so the tests never talk to the network — the dialog
// consumes `useServiceAccounts` for the picker and `useCreateOIDCTrust`
// for the submit mutation.

// Mutable holders that let each test inject a fresh mock behaviour.
const mockMutate = vi.fn();
let mockPending = false;
let mockError: unknown = null;
let mockSAs: ServiceAccount[] = [];

vi.mock("@/lib/api/service-accounts", async () => {
  const actual = await vi.importActual<
    typeof import("@/lib/api/service-accounts")
  >("@/lib/api/service-accounts");
  return {
    ...actual,
    useServiceAccounts: () => ({
      data: mockSAs,
      isLoading: false,
      isError: false,
    }),
  };
});

vi.mock("@/lib/api/oidc-trust", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api/oidc-trust")>(
    "@/lib/api/oidc-trust",
  );
  return {
    ...actual,
    useCreateOIDCTrust: () => ({
      mutateAsync: mockMutate,
      mutate: mockMutate,
      isPending: mockPending,
      error: mockError,
      reset: vi.fn(),
    }),
  };
});

function fixtureSA(id: string, name: string): ServiceAccount {
  return {
    id,
    tenant_id: "tenant-1",
    name,
    description: "",
    allowed_scopes: ["pull", "push"],
    shadow_user_id: "shadow-" + id,
    created_at: "2026-06-30T00:00:00Z",
    active_key_count: 0,
  };
}

function renderDialog(open = true) {
  const onOpenChange = vi.fn();
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const utils = render(
    <QueryClientProvider client={qc}>
      <CreateOIDCTrustDialog open={open} onOpenChange={onOpenChange} />
    </QueryClientProvider>,
  );
  return { ...utils, onOpenChange };
}

describe("CreateOIDCTrustDialog", () => {
  beforeEach(() => {
    mockMutate.mockReset();
    mockMutate.mockResolvedValue({ id: "trust-new" });
    mockPending = false;
    mockError = null;
    mockSAs = [fixtureSA("sa-1", "ci-pipeline"), fixtureSA("sa-2", "builder")];
  });

  it("renders the dialog with all required fields", () => {
    renderDialog();
    expect(
      screen.getByRole("heading", { name: /new .*trust/i }),
    ).toBeInTheDocument();
    expect(screen.getByLabelText(/display name/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/service account/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/issuer url/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/audience/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/subject pattern/i)).toBeInTheDocument();
  });

  it("shows an inline error when the issuer URL doesn't start with https://", async () => {
    const user = userEvent.setup();
    renderDialog();

    await user.type(screen.getByLabelText(/display name/i), "GHA prod");
    await user.type(
      screen.getByLabelText(/issuer url/i),
      "http://token.example.com",
    );
    await user.type(screen.getByLabelText(/audience/i), "registry");
    await user.type(
      screen.getByLabelText(/subject pattern/i),
      "repo:org/*:ref:refs/heads/main",
    );
    await user.click(screen.getByRole("button", { name: /create/i }));

    expect(
      await screen.findByText(/must start with https:\/\//i),
    ).toBeInTheDocument();
    expect(mockMutate).not.toHaveBeenCalled();
  });

  it("shows an inline error when the subject pattern is invalid", async () => {
    const user = userEvent.setup();
    renderDialog();

    await user.type(screen.getByLabelText(/display name/i), "GHA prod");
    await user.type(
      screen.getByLabelText(/issuer url/i),
      "https://token.actions.githubusercontent.com",
    );
    await user.type(screen.getByLabelText(/audience/i), "registry");
    // Three stars — invalid per validateGlobSyntax.
    await user.type(screen.getByLabelText(/subject pattern/i), "repo/***");
    await user.click(screen.getByRole("button", { name: /create/i }));

    expect(
      await screen.findByText(/consecutive '\*'|max 2/i),
    ).toBeInTheDocument();
    expect(mockMutate).not.toHaveBeenCalled();
  });

  it("shows an inline error when display name is blank", async () => {
    const user = userEvent.setup();
    renderDialog();

    // Leave display name blank; fill the rest.
    await user.type(
      screen.getByLabelText(/issuer url/i),
      "https://token.actions.githubusercontent.com",
    );
    await user.type(screen.getByLabelText(/audience/i), "registry");
    await user.type(
      screen.getByLabelText(/subject pattern/i),
      "repo:org/*",
    );
    await user.click(screen.getByRole("button", { name: /create/i }));

    expect(
      await screen.findByText(/display name is required/i),
    ).toBeInTheDocument();
    expect(mockMutate).not.toHaveBeenCalled();
  });

  it("calls the create mutation with the form payload on happy-path submit", async () => {
    const user = userEvent.setup();
    const { onOpenChange } = renderDialog();

    await user.type(screen.getByLabelText(/display name/i), "GHA prod");
    // Service-account picker is a native <select> — set its value directly.
    await user.selectOptions(screen.getByLabelText(/service account/i), "sa-1");
    await user.type(
      screen.getByLabelText(/issuer url/i),
      "https://token.actions.githubusercontent.com",
    );
    await user.type(screen.getByLabelText(/audience/i), "registry");
    await user.type(
      screen.getByLabelText(/subject pattern/i),
      "repo:steveokay/oci-janus:ref:refs/heads/main",
    );

    await user.click(screen.getByRole("button", { name: /create/i }));

    await waitFor(() => expect(mockMutate).toHaveBeenCalledTimes(1));
    expect(mockMutate).toHaveBeenCalledWith(
      expect.objectContaining({
        display_name: "GHA prod",
        service_account_id: "sa-1",
        issuer_url: "https://token.actions.githubusercontent.com",
        audience: "registry",
        subject_pattern: "repo:steveokay/oci-janus:ref:refs/heads/main",
        jwks_cache_ttl_seconds: 3600,
      }),
    );
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
  });

  it("surfaces a server error message on mutation failure", async () => {
    const user = userEvent.setup();
    mockError = {
      response: { status: 400, data: { message: "audience already trusted" } },
    };
    mockMutate.mockRejectedValue(mockError);
    renderDialog();

    await user.type(screen.getByLabelText(/display name/i), "GHA prod");
    await user.type(
      screen.getByLabelText(/issuer url/i),
      "https://token.actions.githubusercontent.com",
    );
    await user.type(screen.getByLabelText(/audience/i), "registry");
    await user.type(
      screen.getByLabelText(/subject pattern/i),
      "repo:org/*",
    );
    await user.click(screen.getByRole("button", { name: /create/i }));

    expect(
      await screen.findByText(/audience already trusted/i),
    ).toBeInTheDocument();
  });
});

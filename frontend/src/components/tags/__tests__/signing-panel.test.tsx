import type * as React from "react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import {
  QueryClient,
  QueryClientProvider,
} from "@tanstack/react-query";
import { SigningPanel } from "../signing-panel";
import {
  SIGNING_DISABLED,
  type SignatureResult,
} from "@/lib/api/signature";
import type { ServiceAccount } from "@/lib/api/service-accounts";

// SigningPanel renders SignManifestDialog, which calls useSignManifest ->
// useQueryClient. Wrap every render in a fresh QueryClientProvider so that
// hook has a client even though we never fire the mutation in these tests.
function renderPanel(ui: React.ReactElement) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={qc}>{ui}</QueryClientProvider>,
  );
}

// FUT-009 — SigningPanel signer_id rendering.
//
// We mock useSignature (the signature list) and useServiceAccounts (the SA
// lookup) so no network is hit. The focus is the tag-detail render rule: a
// signer_id that is a UUID matching an SA shadow_user_id renders the SA name;
// a free-form signer_id renders verbatim.

let signatureResult: {
  data?: SignatureResult;
  isLoading: boolean;
  isError: boolean;
  refetch: () => void;
  isFetching: boolean;
};

let serviceAccounts: ServiceAccount[];

vi.mock("@/lib/api/signature", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api/signature")>(
      "@/lib/api/signature",
    );
  return {
    ...actual,
    useSignature: () => signatureResult,
  };
});

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
  signatureResult = {
    isLoading: false,
    isError: false,
    refetch: () => {},
    isFetching: false,
  };
  serviceAccounts = [makeSA()];
});

describe("SigningPanel signer rendering (FUT-009)", () => {
  it("renders the service-account display name when signer_id is its shadow UUID", () => {
    signatureResult.data = {
      manifest_digest: "sha256:abc",
      signed: true,
      signatures: [
        {
          signer_id: SA_SHADOW,
          key_id: "k1",
          signature_digest: "sha256:sig1",
          signed_at: "2026-07-11T00:00:00Z",
        },
      ],
    };

    renderPanel(<SigningPanel org="acme" repo="api" tag="v1.0" />);

    // The SA name is shown, the raw UUID is not rendered as the signer label.
    expect(screen.getByText("ci-prod-bot")).toBeInTheDocument();
    expect(screen.getByText("Service account")).toBeInTheDocument();
    // The raw UUID should not appear as visible text (it lives in the title
    // attribute only).
    expect(screen.queryByText(SA_SHADOW)).not.toBeInTheDocument();
  });

  it("renders a free-form signer_id verbatim", () => {
    signatureResult.data = {
      manifest_digest: "sha256:abc",
      signed: true,
      signatures: [
        {
          signer_id: "ci-bot",
          key_id: "k1",
          signature_digest: "sha256:sig1",
          signed_at: "2026-07-11T00:00:00Z",
        },
      ],
    };

    renderPanel(<SigningPanel org="acme" repo="api" tag="v1.0" />);

    expect(screen.getByText("ci-bot")).toBeInTheDocument();
    // No "Service account" badge for a free-form signer.
    expect(screen.queryByText("Service account")).not.toBeInTheDocument();
  });

  it("falls back to the raw UUID when the SA can't be resolved (e.g. deleted)", () => {
    // A UUID signer_id with no matching SA in the lookup.
    serviceAccounts = [];
    signatureResult.data = {
      manifest_digest: "sha256:abc",
      signed: true,
      signatures: [
        {
          signer_id: SA_SHADOW,
          key_id: "k1",
          signature_digest: "sha256:sig1",
          signed_at: "2026-07-11T00:00:00Z",
        },
      ],
    };

    renderPanel(<SigningPanel org="acme" repo="api" tag="v1.0" />);

    // Unresolved UUID renders verbatim (never blank) and gets no SA badge.
    expect(screen.getByText(SA_SHADOW)).toBeInTheDocument();
    expect(screen.queryByText("Service account")).not.toBeInTheDocument();
  });

  it("renders the disabled/unsigned states without crashing on the SA lookup", () => {
    signatureResult.data = SIGNING_DISABLED;
    renderPanel(<SigningPanel org="acme" repo="api" tag="v1.0" />);
    expect(screen.getByText(/isn't wired to a signer service/i)).toBeInTheDocument();
  });
});

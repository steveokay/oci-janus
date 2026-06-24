import * as React from "react";
import { render, screen, act } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, test, expect, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

// FUT-018 — SigningTab rendering contract.
//
// Pinning here:
//   • SIGNING_DISABLED sentinel → Disabled card (signer unwired copy).
//   • signed=false → Unsigned card + "Sign with default key" CTA.
//   • signed=true  → SignedCard + list of signature rows + "Sign again".
//   • Mutation invokes useSignByDigest with the right payload shape.

const useSignaturesByDigestMock = vi.fn();
const mutateAsyncMock = vi.fn().mockResolvedValue(undefined);
const useSignByDigestMock = vi.fn(() => ({
  mutateAsync: mutateAsyncMock,
  isPending: false,
}));

vi.mock("@/lib/api/proxy-cache", async () => {
  const actual = await vi.importActual<
    typeof import("@/lib/api/proxy-cache")
  >("@/lib/api/proxy-cache");
  return {
    ...actual,
    useSignaturesByDigest: (digest: string | undefined) =>
      useSignaturesByDigestMock(digest),
    useSignByDigest: () => useSignByDigestMock(),
  };
});

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

// CopyButton uses navigator.clipboard.writeText; stub it so jsdom is
// happy when the SignedCard renders the digest copy affordance.
Object.defineProperty(navigator, "clipboard", {
  value: { writeText: vi.fn().mockResolvedValue(undefined) },
  writable: true,
  configurable: true,
});

import { SigningTab } from "../signing-tab";
import { SIGNING_DISABLED } from "@/lib/api/proxy-cache";

const DIGEST = "sha256:" + "a".repeat(64);

function wrap(node: React.ReactNode): React.ReactElement {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return (
    <QueryClientProvider client={client}>
      {node}
    </QueryClientProvider>
  );
}

describe("SigningTab", () => {
  beforeEach(() => {
    useSignaturesByDigestMock.mockReset();
    useSignByDigestMock.mockClear();
    mutateAsyncMock.mockClear();
  });

  test("renders Disabled card on SIGNING_DISABLED", () => {
    useSignaturesByDigestMock.mockReturnValue({
      data: SIGNING_DISABLED,
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    });

    render(wrap(<SigningTab digest={DIGEST} />));

    expect(screen.getByText(/SIGNER_GRPC_ADDR/)).toBeInTheDocument();
    expect(screen.getByText(/Disabled/i)).toBeInTheDocument();
  });

  test("renders Unsigned state with 'Sign with default key' button", () => {
    useSignaturesByDigestMock.mockReturnValue({
      data: { manifest_digest: DIGEST, signed: false, signatures: [] },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    });

    render(wrap(<SigningTab digest={DIGEST} />));

    expect(screen.getByText(/Unsigned/)).toBeInTheDocument();
    expect(screen.getByTestId("sign-default-button")).toBeInTheDocument();
  });

  test("clicking 'Sign with default key' opens the dialog", async () => {
    const user = userEvent.setup();
    useSignaturesByDigestMock.mockReturnValue({
      data: { manifest_digest: DIGEST, signed: false, signatures: [] },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    });

    render(wrap(<SigningTab digest={DIGEST} />));

    await act(async () => {
      await user.click(screen.getByTestId("sign-default-button"));
    });

    // Dialog title pops up after click — confirms the open state flipped.
    expect(screen.getByText(/Sign cached manifest/i)).toBeInTheDocument();
  });

  test("dialog submit fires mutation with empty signer_id by default", async () => {
    const user = userEvent.setup();
    useSignaturesByDigestMock.mockReturnValue({
      data: { manifest_digest: DIGEST, signed: false, signatures: [] },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    });

    render(wrap(<SigningTab digest={DIGEST} />));

    await act(async () => {
      await user.click(screen.getByTestId("sign-default-button"));
    });
    // Confirm fires the mutation with no signer_id (uses workspace default).
    await act(async () => {
      await user.click(screen.getByTestId("sign-dialog-confirm"));
    });

    expect(mutateAsyncMock).toHaveBeenCalledWith({
      digest: DIGEST,
      signer_id: undefined,
    });
  });

  test("dialog submit fires mutation with typed signer_id when provided", async () => {
    const user = userEvent.setup();
    useSignaturesByDigestMock.mockReturnValue({
      data: { manifest_digest: DIGEST, signed: false, signatures: [] },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    });

    render(wrap(<SigningTab digest={DIGEST} />));

    await act(async () => {
      await user.click(screen.getByTestId("sign-default-button"));
    });

    const input = screen.getByTestId("sign-dialog-signer-id");
    await act(async () => {
      await user.type(input, "alice");
    });
    await act(async () => {
      await user.click(screen.getByTestId("sign-dialog-confirm"));
    });

    expect(mutateAsyncMock).toHaveBeenCalledWith({
      digest: DIGEST,
      signer_id: "alice",
    });
  });

  test("renders signed state with one signature row + 'Sign again' button", () => {
    useSignaturesByDigestMock.mockReturnValue({
      data: {
        manifest_digest: DIGEST,
        signed: true,
        signatures: [
          {
            signer_id: "registry-signer",
            key_id: "key-1",
            signature_digest: "sha256:" + "b".repeat(64),
            signed_at: "2026-06-23T00:00:00Z",
          },
        ],
      },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    });

    render(wrap(<SigningTab digest={DIGEST} />));

    expect(screen.getByText(/Signed/)).toBeInTheDocument();
    expect(screen.getByText("registry-signer")).toBeInTheDocument();
    expect(screen.getByText("key-1")).toBeInTheDocument();
    expect(screen.getByTestId("sign-again-button")).toBeInTheDocument();
  });

  test("renders ErrorState when isError", () => {
    useSignaturesByDigestMock.mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
      error: new Error("boom"),
      refetch: vi.fn(),
    });

    render(wrap(<SigningTab digest={DIGEST} />));

    expect(screen.getByText(/Couldn't load signing status/i)).toBeInTheDocument();
  });

  test("renders a skeleton card while loading", () => {
    useSignaturesByDigestMock.mockReturnValue({
      data: undefined,
      isLoading: true,
      isError: false,
      refetch: vi.fn(),
    });

    const { container } = render(wrap(<SigningTab digest={DIGEST} />));
    expect(container.querySelectorAll(".skeleton-shimmer").length).toBeGreaterThan(0);
  });
});

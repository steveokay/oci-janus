import * as React from "react";
import { render, screen } from "@testing-library/react";
import { describe, test, expect, vi, beforeEach } from "vitest";

// FUT-018 — Severity + Signed cell rendering on the cache list table.
//
// We render <SeverityCell /> + <SignedCell /> in isolation with mocked
// hooks so the test pins each rendering branch (loading, no-scan,
// scanning, complete-clean, complete-critical, signed, unsigned,
// disabled) without bringing up the full route.

const useScanByDigestMock = vi.fn();
const useSignaturesByDigestMock = vi.fn();
vi.mock("@/lib/api/proxy-cache", async () => {
  const actual = await vi.importActual<
    typeof import("@/lib/api/proxy-cache")
  >("@/lib/api/proxy-cache");
  return {
    ...actual,
    useScanByDigest: (digest: string) => useScanByDigestMock(digest),
    useSignaturesByDigest: (digest: string) =>
      useSignaturesByDigestMock(digest),
  };
});

// Stub TanStack Router primitives so importing the route file doesn't
// blow up — we never render the page component itself, only the cell
// components exported alongside it.
vi.mock("@tanstack/react-router", async () => {
  const actual = await vi.importActual<Record<string, unknown>>(
    "@tanstack/react-router",
  );
  return {
    ...actual,
    Link: ({ children }: { children: React.ReactNode }) =>
      React.createElement("a", null, children),
    createFileRoute: () => () => ({ useParams: () => ({}) }),
  };
});

import {
  SeverityCell,
  SignedCell,
} from "../_authenticated.workspace.proxy-cache";
import { SIGNING_DISABLED } from "@/lib/api/proxy-cache";

const DIGEST = "sha256:" + "a".repeat(64);

describe("SeverityCell", () => {
  beforeEach(() => {
    useScanByDigestMock.mockReset();
  });

  test("renders a skeleton while loading", () => {
    useScanByDigestMock.mockReturnValue({ data: undefined, isLoading: true });
    const { container } = render(<SeverityCell digest={DIGEST} />);
    expect(container.querySelectorAll(".skeleton-shimmer").length).toBeGreaterThan(0);
  });

  test("renders em-dash when no scan exists (data === null)", () => {
    useScanByDigestMock.mockReturnValue({ data: null, isLoading: false });
    render(<SeverityCell digest={DIGEST} />);
    expect(screen.getByTestId("severity-cell-none")).toBeInTheDocument();
    // Em-dash glyph is in the cell body.
    expect(screen.getByTestId("severity-cell-none").textContent).toContain("—");
  });

  test("renders Scanning pill for status=running", () => {
    useScanByDigestMock.mockReturnValue({
      data: { status: "running", severity_counts: {} },
      isLoading: false,
    });
    render(<SeverityCell digest={DIGEST} />);
    expect(screen.getByTestId("severity-cell-scanning")).toBeInTheDocument();
  });

  test("renders Critical badge with count for critical-tier scan", () => {
    useScanByDigestMock.mockReturnValue({
      data: {
        status: "complete",
        severity_counts: { CRITICAL: 2, HIGH: 3, MEDIUM: 1 },
      },
      isLoading: false,
    });
    render(<SeverityCell digest={DIGEST} />);
    const badge = screen.getByTestId("severity-cell-critical");
    expect(badge).toBeInTheDocument();
    // Critical wins precedence so the badge reads "Critical (2)".
    expect(badge.textContent).toContain("Critical");
    expect(badge.textContent).toContain("2");
  });

  test("renders High when only HIGH+ severities present (no critical)", () => {
    useScanByDigestMock.mockReturnValue({
      data: { status: "complete", severity_counts: { HIGH: 5 } },
      isLoading: false,
    });
    render(<SeverityCell digest={DIGEST} />);
    expect(screen.getByTestId("severity-cell-high")).toBeInTheDocument();
  });

  test("renders Clean when complete with zero findings", () => {
    useScanByDigestMock.mockReturnValue({
      data: { status: "complete", severity_counts: {} },
      isLoading: false,
    });
    render(<SeverityCell digest={DIGEST} />);
    expect(screen.getByTestId("severity-cell-clean")).toBeInTheDocument();
  });

  test("renders Failed pill for status=failed", () => {
    useScanByDigestMock.mockReturnValue({
      data: { status: "failed", severity_counts: {} },
      isLoading: false,
    });
    render(<SeverityCell digest={DIGEST} />);
    expect(screen.getByTestId("severity-cell-failed")).toBeInTheDocument();
  });

  test("renders em-dash when digest is empty (legacy row)", () => {
    useScanByDigestMock.mockReturnValue({
      data: { status: "complete", severity_counts: { CRITICAL: 1 } },
      isLoading: false,
    });
    render(<SeverityCell digest="" />);
    expect(screen.getByTestId("severity-cell-none")).toBeInTheDocument();
  });
});

describe("SignedCell", () => {
  beforeEach(() => {
    useSignaturesByDigestMock.mockReset();
  });

  test("renders a skeleton while loading", () => {
    useSignaturesByDigestMock.mockReturnValue({
      data: undefined,
      isLoading: true,
    });
    const { container } = render(<SignedCell digest={DIGEST} />);
    expect(container.querySelectorAll(".skeleton-shimmer").length).toBeGreaterThan(0);
  });

  test("renders em-dash when SIGNING_DISABLED (route off)", () => {
    useSignaturesByDigestMock.mockReturnValue({
      data: SIGNING_DISABLED,
      isLoading: false,
    });
    render(<SignedCell digest={DIGEST} />);
    expect(screen.getByTestId("signed-cell-none")).toBeInTheDocument();
  });

  test("renders em-dash for unsigned digest", () => {
    useSignaturesByDigestMock.mockReturnValue({
      data: { manifest_digest: DIGEST, signed: false, signatures: [] },
      isLoading: false,
    });
    render(<SignedCell digest={DIGEST} />);
    expect(screen.getByTestId("signed-cell-none")).toBeInTheDocument();
  });

  test("renders a check icon when ≥1 signature", () => {
    useSignaturesByDigestMock.mockReturnValue({
      data: {
        manifest_digest: DIGEST,
        signed: true,
        signatures: [
          {
            signer_id: "alice",
            key_id: "k1",
            signature_digest: "sha256:b",
            signed_at: "2026-06-23T00:00:00Z",
          },
        ],
      },
      isLoading: false,
    });
    render(<SignedCell digest={DIGEST} />);
    expect(screen.getByTestId("signed-cell-signed")).toBeInTheDocument();
  });

  test("renders +N suffix when 2 or more signatures", () => {
    useSignaturesByDigestMock.mockReturnValue({
      data: {
        manifest_digest: DIGEST,
        signed: true,
        signatures: [
          {
            signer_id: "alice",
            key_id: "k1",
            signature_digest: "sha256:b",
            signed_at: "2026-06-23T00:00:00Z",
          },
          {
            signer_id: "bob",
            key_id: "k2",
            signature_digest: "sha256:c",
            signed_at: "2026-06-23T00:00:00Z",
          },
          {
            signer_id: "carol",
            key_id: "k3",
            signature_digest: "sha256:d",
            signed_at: "2026-06-23T00:00:00Z",
          },
        ],
      },
      isLoading: false,
    });
    render(<SignedCell digest={DIGEST} />);
    const cell = screen.getByTestId("signed-cell-signed");
    // 3 signatures → "+2" affordance.
    expect(cell.textContent).toContain("+2");
  });

  test("renders em-dash when digest is empty", () => {
    useSignaturesByDigestMock.mockReturnValue({
      data: { manifest_digest: "", signed: true, signatures: [{}] },
      isLoading: false,
    });
    render(<SignedCell digest="" />);
    expect(screen.getByTestId("signed-cell-none")).toBeInTheDocument();
  });
});

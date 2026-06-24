import * as React from "react";
import { render, screen, waitFor, act } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, test, expect, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

// FUT-018 — ScansTab rendering contract.
//
// Mocks useScanByDigest + useTriggerScanByDigest directly so the test
// drives the visible state without going through axios + the router.
// Pinning here:
//   • No-scan → EmptyState + "Trigger scan" button (calls mutation).
//   • Pending/running → "Scanning…" pill in the in-flight card.
//   • Complete → SeverityBar + per-severity legend + Rescan button.
//   • Failed → red banner + retry button.

const useScanByDigestMock = vi.fn();
const mutateAsyncMock = vi.fn().mockResolvedValue(undefined);
const useTriggerScanByDigestMock = vi.fn(() => ({
  mutateAsync: mutateAsyncMock,
  isPending: false,
}));
vi.mock("@/lib/api/proxy-cache", async () => {
  const actual = await vi.importActual<
    typeof import("@/lib/api/proxy-cache")
  >("@/lib/api/proxy-cache");
  return {
    ...actual,
    useScanByDigest: (digest: string | undefined) =>
      useScanByDigestMock(digest),
    useTriggerScanByDigest: () => useTriggerScanByDigestMock(),
  };
});

// Sonner toast — no-op so unhandled toast calls don't blow up jsdom.
vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

import { ScansTab } from "../scans-tab";

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

describe("ScansTab", () => {
  beforeEach(() => {
    useScanByDigestMock.mockReset();
    useTriggerScanByDigestMock.mockClear();
    mutateAsyncMock.mockClear();
  });

  test("renders 'No vulnerability scan yet' empty state when data is null", () => {
    useScanByDigestMock.mockReturnValue({
      data: null,
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    });

    render(wrap(<ScansTab digest={DIGEST} />));

    expect(
      screen.getByText(/No vulnerability scan yet/i),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Trigger scan/i }),
    ).toBeInTheDocument();
  });

  test("clicking Trigger scan fires the mutation with the digest", async () => {
    const user = userEvent.setup();
    useScanByDigestMock.mockReturnValue({
      data: null,
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    });

    render(wrap(<ScansTab digest={DIGEST} />));

    await act(async () => {
      await user.click(screen.getByRole("button", { name: /Trigger scan/i }));
    });

    expect(mutateAsyncMock).toHaveBeenCalledWith(DIGEST);
  });

  test("renders Scanning pill while status=running", () => {
    useScanByDigestMock.mockReturnValue({
      data: {
        scan_id: "s1",
        status: "running",
        scanner_name: "trivy",
        scanner_version: "0.50",
        severity_counts: {},
        started_at: new Date().toISOString(),
        completed_at: undefined,
      },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    });

    render(wrap(<ScansTab digest={DIGEST} />));

    expect(screen.getByText(/Scanning/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Trigger scan/i })).not.toBeInTheDocument();
  });

  test("renders Queued pill while status=pending", () => {
    useScanByDigestMock.mockReturnValue({
      data: {
        scan_id: "s1",
        status: "pending",
        scanner_name: "trivy",
        scanner_version: "0.50",
        severity_counts: {},
        started_at: new Date().toISOString(),
        completed_at: undefined,
      },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    });

    render(wrap(<ScansTab digest={DIGEST} />));

    expect(screen.getByText(/Queued/i)).toBeInTheDocument();
  });

  test("renders the complete card with severity legend + Rescan button", async () => {
    const user = userEvent.setup();
    useScanByDigestMock.mockReturnValue({
      data: {
        scan_id: "s1",
        status: "complete",
        scanner_name: "trivy",
        scanner_version: "0.50",
        severity_counts: { CRITICAL: 2, HIGH: 3, MEDIUM: 1 },
        started_at: "2026-06-20T00:00:00Z",
        completed_at: "2026-06-20T00:01:00Z",
      },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    });

    render(wrap(<ScansTab digest={DIGEST} />));

    // Total = 6 findings → "6 findings" headline.
    expect(screen.getByText(/6 findings/i)).toBeInTheDocument();
    // Severity legend renders each category with its count. Use a
    // flexible matcher so a "2 critical" rendering survives whitespace.
    expect(screen.getByText("2")).toBeInTheDocument();
    expect(screen.getByText(/critical/i)).toBeInTheDocument();
    expect(screen.getByText(/high/i)).toBeInTheDocument();
    expect(screen.getByText(/medium/i)).toBeInTheDocument();

    // Rescan button is present and re-fires the mutation.
    const rescan = screen.getByTestId("rescan-button");
    await act(async () => {
      await user.click(rescan);
    });
    expect(mutateAsyncMock).toHaveBeenCalledWith(DIGEST);
  });

  test("renders Clean badge when complete with zero findings", () => {
    useScanByDigestMock.mockReturnValue({
      data: {
        scan_id: "s1",
        status: "complete",
        scanner_name: "trivy",
        scanner_version: "0.50",
        severity_counts: {},
        started_at: "2026-06-20T00:00:00Z",
        completed_at: "2026-06-20T00:01:00Z",
      },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    });

    render(wrap(<ScansTab digest={DIGEST} />));

    expect(screen.getByText(/Clean — no vulnerabilities found/i)).toBeInTheDocument();
  });

  test("renders the failed banner with retry button on status=failed", async () => {
    const user = userEvent.setup();
    useScanByDigestMock.mockReturnValue({
      data: {
        scan_id: "s1",
        status: "failed",
        scanner_name: "trivy",
        scanner_version: "0.50",
        severity_counts: {},
        started_at: "2026-06-20T00:00:00Z",
        completed_at: undefined,
      },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    });

    render(wrap(<ScansTab digest={DIGEST} />));

    expect(screen.getByText(/last scan didn't complete/i)).toBeInTheDocument();
    const retry = screen.getByRole("button", { name: /Trigger scan again/i });
    await act(async () => {
      await user.click(retry);
    });
    expect(mutateAsyncMock).toHaveBeenCalledWith(DIGEST);
  });

  test("renders a skeleton card while loading", () => {
    useScanByDigestMock.mockReturnValue({
      data: undefined,
      isLoading: true,
      isError: false,
      refetch: vi.fn(),
    });

    const { container } = render(wrap(<ScansTab digest={DIGEST} />));
    // Skeleton primitive applies the `skeleton-shimmer` class — assert
    // at least one shimmer element rendered.
    expect(container.querySelectorAll(".skeleton-shimmer").length).toBeGreaterThan(0);
  });

  test("renders ErrorState on isError", async () => {
    const refetch = vi.fn();
    useScanByDigestMock.mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
      error: new Error("boom"),
      refetch,
    });

    render(wrap(<ScansTab digest={DIGEST} />));

    expect(screen.getByText(/Couldn't load scan/i)).toBeInTheDocument();
    const retry = screen.getByRole("button", { name: /retry/i });
    await act(async () => {
      retry.click();
    });
    await waitFor(() => expect(refetch).toHaveBeenCalled());
  });
});

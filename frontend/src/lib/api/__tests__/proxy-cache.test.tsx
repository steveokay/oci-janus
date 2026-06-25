import * as React from "react";
import { renderHook, waitFor } from "@testing-library/react";
import { describe, test, expect, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { AxiosError } from "axios";

// FUT-013 — proxy-cache hook contract.
//
// The probe-and-hide pattern (sidebar entry hidden when PROXY_GRPC_ADDR
// is unset on the BFF) hangs off useCacheStats() returning undefined on
// 403/404. If a future refactor accidentally turns those statuses into
// thrown errors, the sidebar would crash and the route would render a
// generic error instead of the friendly "feature not available"
// EmptyState. This file pins the contract.

const getMock = vi.fn();
vi.mock("../client", () => ({
  apiClient: {
    get: (...args: unknown[]) => getMock(...args),
    delete: vi.fn(),
  },
}));

// Disable retries so 4xx responses don't trigger React-Query backoff
// and stretch the test out.
function wrapper(): React.FC<{ children: React.ReactNode }> {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  function Wrap({ children }: { children: React.ReactNode }) {
    return React.createElement(QueryClientProvider, { client }, children);
  }
  return Wrap;
}

function axiosErrorFromStatus(status: number): AxiosError {
  const err = new AxiosError(`Request failed with status code ${status}`);
  err.response = {
    status,
    statusText: "",
    headers: {},
    config: {} as never,
    data: {},
  };
  return err;
}

describe("useCacheStats — probe-and-hide contract", () => {
  beforeEach(() => {
    getMock.mockReset();
  });

  test("resolves to the parsed body on 200", async () => {
    const { useCacheStats } = await import("../proxy-cache");
    getMock.mockResolvedValueOnce({
      data: {
        total_manifests: 12,
        total_bytes: 8 * 1024,
        unique_upstreams: 2,
        total_pulls: 47,
      },
    });

    const { result } = renderHook(() => useCacheStats(), { wrapper: wrapper() });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual({
      total_manifests: 12,
      total_bytes: 8 * 1024,
      unique_upstreams: 2,
      total_pulls: 47,
    });
  });

  test("resolves to undefined on 404 (PROXY_GRPC_ADDR unset)", async () => {
    const { useCacheStats } = await import("../proxy-cache");
    getMock.mockRejectedValueOnce(axiosErrorFromStatus(404));

    const { result } = renderHook(() => useCacheStats(), { wrapper: wrapper() });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    // Critical contract: undefined, NOT a thrown error. The sidebar uses
    // `data !== undefined` to gate visibility.
    expect(result.current.data).toBeNull();
    expect(result.current.isError).toBe(false);
  });

  test("resolves to undefined on 403 (caller is not workspace-admin)", async () => {
    const { useCacheStats } = await import("../proxy-cache");
    getMock.mockRejectedValueOnce(axiosErrorFromStatus(403));

    const { result } = renderHook(() => useCacheStats(), { wrapper: wrapper() });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeNull();
  });

  test("propagates 500 as an error (real backend failure stays visible)", async () => {
    const { useCacheStats } = await import("../proxy-cache");
    getMock.mockRejectedValueOnce(axiosErrorFromStatus(500));

    const { result } = renderHook(() => useCacheStats(), { wrapper: wrapper() });
    await waitFor(() => expect(result.current.isError).toBe(true));
    // 5xx must NOT be swallowed — the route renders ErrorState in that case.
    expect(result.current.data).toBeUndefined();
    expect(result.current.error).toBeDefined();
  });
});

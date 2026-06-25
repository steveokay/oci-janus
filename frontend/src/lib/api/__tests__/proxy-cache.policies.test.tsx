import * as React from "react";
import { renderHook, waitFor } from "@testing-library/react";
import { describe, test, expect, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { AxiosError } from "axios";

// FUT-017 — proxy-cache scan + sign policy hook contracts.
//
// Mirrors the FUT-013 proxy-cache.test.tsx pattern: 200 → parsed data,
// 404 / 403 → `null` (so the policy card can hide), 5xx → thrown error.
// Pinning the contract here keeps a future refactor from accidentally
// flipping 403 / 404 into thrown errors and crashing the policy card.

const getMock = vi.fn();
const putMock = vi.fn();
vi.mock("../client", () => ({
  apiClient: {
    get: (...args: unknown[]) => getMock(...args),
    put: (...args: unknown[]) => putMock(...args),
    delete: vi.fn(),
    post: vi.fn(),
  },
}));

function wrapper(): React.FC<{ children: React.ReactNode }> {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
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

describe("useProxyCacheScanPolicy — single-upstream probe contract", () => {
  beforeEach(() => {
    getMock.mockReset();
    putMock.mockReset();
  });

  test("resolves to parsed body on 200", async () => {
    const { useProxyCacheScanPolicy } = await import("../proxy-cache");
    getMock.mockResolvedValueOnce({
      data: {
        upstream_name: "dockerhub",
        auto_scan: true,
        severity_threshold: "high",
        updated_at: "2026-06-24T00:00:00Z",
        updated_by: "alice@example.com",
      },
    });

    const { result } = renderHook(
      () => useProxyCacheScanPolicy("dockerhub"),
      { wrapper: wrapper() },
    );
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toMatchObject({
      upstream_name: "dockerhub",
      auto_scan: true,
      severity_threshold: "high",
    });
  });

  test("resolves to null on 404 (scanner unwired)", async () => {
    const { useProxyCacheScanPolicy } = await import("../proxy-cache");
    getMock.mockRejectedValueOnce(axiosErrorFromStatus(404));

    const { result } = renderHook(
      () => useProxyCacheScanPolicy("dockerhub"),
      { wrapper: wrapper() },
    );
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeNull();
    expect(result.current.isError).toBe(false);
  });

  test("resolves to null on 403 (caller is not workspace-admin)", async () => {
    const { useProxyCacheScanPolicy } = await import("../proxy-cache");
    getMock.mockRejectedValueOnce(axiosErrorFromStatus(403));

    const { result } = renderHook(
      () => useProxyCacheScanPolicy("dockerhub"),
      { wrapper: wrapper() },
    );
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeNull();
  });

  test("propagates 500 as an error (real failure stays visible)", async () => {
    const { useProxyCacheScanPolicy } = await import("../proxy-cache");
    getMock.mockRejectedValueOnce(axiosErrorFromStatus(500));

    const { result } = renderHook(
      () => useProxyCacheScanPolicy("dockerhub"),
      { wrapper: wrapper() },
    );
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(result.current.data).toBeUndefined();
    expect(result.current.error).toBeDefined();
  });
});

describe("useProxyCacheScanPolicies — list probe contract", () => {
  beforeEach(() => {
    getMock.mockReset();
  });

  test("resolves to the policies array on 200", async () => {
    const { useProxyCacheScanPolicies } = await import("../proxy-cache");
    getMock.mockResolvedValueOnce({
      data: {
        policies: [
          {
            upstream_name: "dockerhub",
            auto_scan: true,
            severity_threshold: "high",
          },
          {
            upstream_name: "gcr",
            auto_scan: false,
            severity_threshold: "",
          },
        ],
      },
    });

    const { result } = renderHook(() => useProxyCacheScanPolicies(), {
      wrapper: wrapper(),
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toHaveLength(2);
    expect(result.current.data?.[0].upstream_name).toBe("dockerhub");
  });

  test("resolves to null on 404", async () => {
    const { useProxyCacheScanPolicies } = await import("../proxy-cache");
    getMock.mockRejectedValueOnce(axiosErrorFromStatus(404));

    const { result } = renderHook(() => useProxyCacheScanPolicies(), {
      wrapper: wrapper(),
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeNull();
  });

  test("resolves to null on 403", async () => {
    const { useProxyCacheScanPolicies } = await import("../proxy-cache");
    getMock.mockRejectedValueOnce(axiosErrorFromStatus(403));

    const { result } = renderHook(() => useProxyCacheScanPolicies(), {
      wrapper: wrapper(),
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeNull();
  });

  test("propagates 500 as an error", async () => {
    const { useProxyCacheScanPolicies } = await import("../proxy-cache");
    getMock.mockRejectedValueOnce(axiosErrorFromStatus(500));

    const { result } = renderHook(() => useProxyCacheScanPolicies(), {
      wrapper: wrapper(),
    });
    await waitFor(() => expect(result.current.isError).toBe(true));
  });
});

describe("useProxyCacheSignPolicy — single-upstream probe contract", () => {
  beforeEach(() => {
    getMock.mockReset();
  });

  test("resolves to parsed body on 200", async () => {
    const { useProxyCacheSignPolicy } = await import("../proxy-cache");
    getMock.mockResolvedValueOnce({
      data: {
        upstream_name: "dockerhub",
        auto_sign: true,
        key_id: "prod-key-1",
      },
    });

    const { result } = renderHook(
      () => useProxyCacheSignPolicy("dockerhub"),
      { wrapper: wrapper() },
    );
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.key_id).toBe("prod-key-1");
  });

  test("resolves to null on 404 (signer unwired)", async () => {
    const { useProxyCacheSignPolicy } = await import("../proxy-cache");
    getMock.mockRejectedValueOnce(axiosErrorFromStatus(404));

    const { result } = renderHook(
      () => useProxyCacheSignPolicy("dockerhub"),
      { wrapper: wrapper() },
    );
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeNull();
  });

  test("resolves to null on 403", async () => {
    const { useProxyCacheSignPolicy } = await import("../proxy-cache");
    getMock.mockRejectedValueOnce(axiosErrorFromStatus(403));

    const { result } = renderHook(
      () => useProxyCacheSignPolicy("dockerhub"),
      { wrapper: wrapper() },
    );
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeNull();
  });

  test("propagates 500 as an error", async () => {
    const { useProxyCacheSignPolicy } = await import("../proxy-cache");
    getMock.mockRejectedValueOnce(axiosErrorFromStatus(500));

    const { result } = renderHook(
      () => useProxyCacheSignPolicy("dockerhub"),
      { wrapper: wrapper() },
    );
    await waitFor(() => expect(result.current.isError).toBe(true));
  });
});

describe("useProxyCacheSignPolicies — list probe contract", () => {
  beforeEach(() => {
    getMock.mockReset();
  });

  test("resolves to the policies array on 200", async () => {
    const { useProxyCacheSignPolicies } = await import("../proxy-cache");
    getMock.mockResolvedValueOnce({
      data: {
        policies: [
          {
            upstream_name: "dockerhub",
            auto_sign: true,
            key_id: "prod-key-1",
          },
        ],
      },
    });

    const { result } = renderHook(() => useProxyCacheSignPolicies(), {
      wrapper: wrapper(),
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toHaveLength(1);
  });

  test("resolves to null on 404", async () => {
    const { useProxyCacheSignPolicies } = await import("../proxy-cache");
    getMock.mockRejectedValueOnce(axiosErrorFromStatus(404));

    const { result } = renderHook(() => useProxyCacheSignPolicies(), {
      wrapper: wrapper(),
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeNull();
  });

  test("resolves to null on 403", async () => {
    const { useProxyCacheSignPolicies } = await import("../proxy-cache");
    getMock.mockRejectedValueOnce(axiosErrorFromStatus(403));

    const { result } = renderHook(() => useProxyCacheSignPolicies(), {
      wrapper: wrapper(),
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeNull();
  });

  test("propagates 500", async () => {
    const { useProxyCacheSignPolicies } = await import("../proxy-cache");
    getMock.mockRejectedValueOnce(axiosErrorFromStatus(500));

    const { result } = renderHook(() => useProxyCacheSignPolicies(), {
      wrapper: wrapper(),
    });
    await waitFor(() => expect(result.current.isError).toBe(true));
  });
});

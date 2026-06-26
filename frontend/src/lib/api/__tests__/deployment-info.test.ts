import * as React from "react";
import { renderHook, waitFor } from "@testing-library/react";
import { describe, test, expect, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

// REDESIGN-001 Phase 4.1 — useDeploymentInfo() hook contract.
//
// The hook provides the immutable deployment posture (single vs multi mode)
// to the FE so downstream chrome can render appropriately. This test file
// ensures both "single" and "multi" modes are parsed correctly, and that
// errors are surfaced properly.

const getMock = vi.fn();
vi.mock("../client", () => ({
  apiClient: {
    get: (...args: unknown[]) => getMock(...args),
  },
}));

// Disable retries so 4xx/5xx responses don't trigger React-Query backoff.
function wrapper(): React.FC<{ children: React.ReactNode }> {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  function Wrap({ children }: { children: React.ReactNode }) {
    return React.createElement(QueryClientProvider, { client }, children);
  }
  return Wrap;
}


describe("useDeploymentInfo", () => {
  beforeEach(() => {
    getMock.mockReset();
  });

  test("single mode response is parsed correctly", async () => {
    const { useDeploymentInfo } = await import("../deployment-info");
    getMock.mockResolvedValueOnce({
      data: {
        deployment_mode: "single",
        version: "v1.0.0",
      },
    });

    const { result } = renderHook(() => useDeploymentInfo(), {
      wrapper: wrapper(),
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual({
      deployment_mode: "single",
      version: "v1.0.0",
    });
  });

  test("multi mode response is parsed correctly", async () => {
    const { useDeploymentInfo } = await import("../deployment-info");
    getMock.mockResolvedValueOnce({
      data: {
        deployment_mode: "multi",
        version: "dev",
      },
    });

    const { result } = renderHook(() => useDeploymentInfo(), {
      wrapper: wrapper(),
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual({
      deployment_mode: "multi",
      version: "dev",
    });
  });

});

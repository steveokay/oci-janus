import * as React from "react";
import { renderHook, waitFor } from "@testing-library/react";
import { describe, test, expect, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

// useDeploymentInfo() hook contract.
//
// The platform is single-tenant only (ADR-0031, REDESIGN-001 Phase 9) — the
// historical `deployment_mode` flag was removed, so the hook now exposes only
// the build `version` for the Workspace posture card.

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

  test("exposes the build version", async () => {
    const { useDeploymentInfo } = await import("../deployment-info");
    getMock.mockResolvedValueOnce({
      data: { version: "v1.0.0" },
    });

    const { result } = renderHook(() => useDeploymentInfo(), {
      wrapper: wrapper(),
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.version).toBe("v1.0.0");
  });
});

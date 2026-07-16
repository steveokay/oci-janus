import * as React from "react";
import { renderHook, waitFor } from "@testing-library/react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

// useGenerateMcpKey — the MCP one-click mint. This test pins the contract that
// the SA-create POST body stamps origin: "mcp-connect" so the Service Accounts
// list can badge MCP-minted accounts. apiClient is mocked so no network is hit.

const postMock = vi.fn();
vi.mock("../client", () => ({
  apiClient: {
    get: vi.fn(),
    post: (...args: unknown[]) => postMock(...args),
    put: vi.fn(),
    delete: vi.fn(),
  },
}));

function wrapper(): React.FC<{ children: React.ReactNode }> {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
  return function Wrap({ children }: { children: React.ReactNode }) {
    return React.createElement(QueryClientProvider, { client }, children);
  };
}

describe("useGenerateMcpKey — origin stamping", () => {
  beforeEach(() => {
    postMock.mockReset();
  });

  it("stamps origin: 'mcp-connect' on the service-account create body", async () => {
    // 1st POST → SA create; 2nd POST → api-key issue (returns the secret).
    postMock
      .mockResolvedValueOnce({ data: { id: "sa-1", name: "mcp-agent-abc" } })
      .mockResolvedValueOnce({ data: { id: "key-1", key: "secret-value" } });

    const { useGenerateMcpKey } = await import("../mcp");
    const { result } = renderHook(() => useGenerateMcpKey(), {
      wrapper: wrapper(),
    });

    await result.current.mutateAsync(1752600000000);

    await waitFor(() => expect(postMock).toHaveBeenCalledTimes(2));

    // The first call is the SA create — assert its body carries the origin tag.
    const [saPath, saBody] = postMock.mock.calls[0];
    expect(saPath).toBe("/service-accounts");
    expect(saBody).toMatchObject({ origin: "mcp-connect" });
  });
});

// Beacon — useSessions hook unit test.
//
// Mirrors abilities.test.tsx: mock ../client so no network leaves the process,
// render the hook through a fresh QueryClient wrapper, and assert it unwraps
// `{ sessions: [...] }` and calls the right endpoint.

import * as React from "react";
import { renderHook, waitFor } from "@testing-library/react";
import { describe, test, expect, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import { type Session } from "../sessions";

// ---------------------------------------------------------------------------
// Mock the API client so network calls never leave the process.
// ---------------------------------------------------------------------------

const getMock = vi.fn();
vi.mock("../client", () => ({
  apiClient: {
    get: (...args: unknown[]) => getMock(...args),
  },
}));

// wrapper creates a fresh QueryClient for each test so caches don't bleed.
function wrapper(): React.FC<{ children: React.ReactNode }> {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  function Wrap({ children }: { children: React.ReactNode }) {
    return React.createElement(QueryClientProvider, { client }, children);
  }
  return Wrap;
}

describe("useSessions", () => {
  beforeEach(() => {
    getMock.mockReset();
  });

  test("unwraps { sessions: [...] } and calls GET /users/me/sessions", async () => {
    const rows: Session[] = [
      {
        sid: "sess-1",
        device_label: "Chrome on macOS",
        user_agent: "Mozilla/5.0",
        ip: "10.0.0.1",
        created_at: "2026-07-01T10:00:00Z",
        last_active_at: "2026-07-05T09:00:00Z",
        current: true,
      },
    ];
    getMock.mockResolvedValueOnce({ data: { sessions: rows } });

    // Lazily import so the vi.mock() for ../client is active first.
    const { useSessions } = await import("../sessions");
    const { result } = renderHook(() => useSessions(), { wrapper: wrapper() });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual(rows);
    expect(getMock).toHaveBeenCalledWith("/users/me/sessions");
  });

  test("defaults to an empty array when the body has no sessions field", async () => {
    getMock.mockResolvedValueOnce({ data: {} });

    const { useSessions } = await import("../sessions");
    const { result } = renderHook(() => useSessions(), { wrapper: wrapper() });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual([]);
  });
});

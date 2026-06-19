import { QueryClient } from "@tanstack/react-query";

// Beacon — single QueryClient shared by the whole app.
// We keep `staleTime` low (10s) for ops surfaces — operators expect
// numbers to be current. Lists are cached longer with explicit invalidation
// on mutate.
export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 10_000,
      gcTime: 5 * 60_000,
      refetchOnWindowFocus: true,
      retry: (failureCount, error) => {
        // Don't retry 401/403/404 — those are state, not flake.
        const status = (error as { response?: { status?: number } })?.response
          ?.status;
        if (status === 401 || status === 403 || status === 404) return false;
        return failureCount < 2;
      },
    },
    mutations: {
      retry: false,
    },
  },
});

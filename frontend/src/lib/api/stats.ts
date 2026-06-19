import { useQuery } from "@tanstack/react-query";
import { apiClient } from "./client";
import type { StatsResponse } from "./types";

// Stats poll a little harder than the default — operators expect the
// dashboard numbers to feel "live" even without a streaming endpoint.
const STATS_REFRESH_MS = 30_000;

export const statsKeys = {
  all: ["stats"] as const,
};

export function useStats() {
  return useQuery({
    queryKey: statsKeys.all,
    queryFn: async () => {
      const { data } = await apiClient.get<StatsResponse>("/stats");
      return data;
    },
    refetchInterval: STATS_REFRESH_MS,
    staleTime: 10_000,
  });
}

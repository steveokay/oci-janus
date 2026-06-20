import { useQuery } from "@tanstack/react-query";
import { apiClient } from "./client";

// FE-API-031 — per-repo storage breakdown for the calling tenant.
//
// Single endpoint, no pagination: top-50 repos sorted by storage_used DESC
// plus the tenant-wide total. The percent-of-tenant value is computed on
// the BFF so every surface renders identical numbers (no rounding drift).
//
// Cache stays warm for a minute — operators do not push storage every few
// seconds, and the tile sits beside /stats which already refreshes at 30s.

export interface RepositoryStorageEntry {
  repo_id: string;
  org: string;
  name: string;
  storage_used_bytes: number;
  percent_of_tenant: number;
}

export interface StorageBreakdownResponse {
  tenant_storage_used_bytes: number;
  repositories: RepositoryStorageEntry[];
}

export const storageBreakdownKeys = {
  all: ["stats", "storage"] as const,
};

export function useStorageBreakdown() {
  return useQuery({
    queryKey: storageBreakdownKeys.all,
    queryFn: async () => {
      const { data } = await apiClient.get<StorageBreakdownResponse>(
        "/stats/storage",
      );
      return data;
    },
    staleTime: 60_000,
  });
}

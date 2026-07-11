import { useQuery } from "@tanstack/react-query";
import { apiClient } from "./client";

// One environment card's data from GET /api/v1/orgs. Mirrors the BFF
// OrgSummaryResponse. `last_activity_at` is absent when the org has no
// pushed manifests yet.
export interface OrgSummary {
  org_id: string;
  org: string;
  repo_count: number;
  storage_used_bytes: number;
  image_repo_count?: number;
  helm_repo_count?: number;
  last_activity_at?: string;
}

export interface OrgsListResponse {
  orgs: OrgSummary[];
}

export const orgKeys = {
  all: ["orgs"] as const,
  list: () => [...orgKeys.all, "list"] as const,
};

// useOrgs loads the environments overview. Unpaginated — the org count
// per tenant is small by design, so a single GET returns them all.
export function useOrgs() {
  return useQuery({
    queryKey: orgKeys.list(),
    queryFn: async () => {
      const { data } = await apiClient.get<OrgsListResponse>("/orgs");
      return data;
    },
    staleTime: 15_000,
  });
}

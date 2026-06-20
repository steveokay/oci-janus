import { useQuery } from "@tanstack/react-query";
import { apiClient } from "./client";

// FE-API-009 — current-workspace metadata.
//
// Backend:  GET /api/v1/workspace/me  (services/management → tenant gRPC)
//
// Single source of truth for "what tenant am I in, what is it called, what
// hostname do docker clients use, which custom domains have I registered".
// Used by:
//   - Sidebar header (replaces hardcoded "Janus / Registry control")
//   - PullCommandCard (replaces hardcoded `registry.localhost`)
//   - Profile IdentityCard (surfaces tenant name + plan)
//   - Login footer chip (resolves the tenant once we have a JWT)
//
// 404 from the BFF means the management deployment isn't wired to a tenant
// service (TENANT_GRPC_ADDR unset). We return null in that case so callers
// can fall back to JWT-only data without crashing.

export interface WorkspaceDomainEntry {
  domain: string;
  verified: boolean;
  is_primary: boolean;
}

export interface Workspace {
  tenant_id: string;
  name: string;
  slug: string;
  plan: string;
  host: string;
  host_is_custom: boolean;
  domains: WorkspaceDomainEntry[];
  created_at: string;
}

export const workspaceKeys = {
  all: ["workspace"] as const,
};

export function useWorkspace() {
  return useQuery({
    queryKey: workspaceKeys.all,
    queryFn: async (): Promise<Workspace | null> => {
      try {
        const { data } = await apiClient.get<Workspace>("/workspace/me");
        return data;
      } catch (e) {
        const status = (e as { response?: { status?: number } })?.response
          ?.status;
        if (status === 404) return null;
        throw e;
      }
    },
    // 5 min — workspace identity changes very rarely (rename / plan
    // change / custom-domain promote). Cheaper than re-fetching per tab.
    staleTime: 5 * 60_000,
  });
}

import { useQuery } from "@tanstack/react-query";
import { apiClient } from "./client";

// RegistryInfo mirrors the JSON returned by GET /api/v1/registry-info
// (services/management/internal/handler/registry_info.go).
export interface RegistryInfo {
  registry_host: string;
  supports_oci_v1_1: boolean;
}

// useRegistryInfo fetches the deployment's externally-reachable registry
// hostname for use in the credential-helpers (/api-keys/helpers) surface.
// Aggressively cached (10-minute staleTime) — the hostname doesn't change
// during a session.
//
// Note: apiClient is an axios instance, so we destructure `data` from the
// response (matching the pattern used by service-accounts.ts).
export function useRegistryInfo() {
  return useQuery<RegistryInfo>({
    queryKey: ["registry-info"],
    queryFn: async () => {
      const { data } = await apiClient.get<RegistryInfo>("/registry-info");
      return data;
    },
    staleTime: 10 * 60 * 1000,
    gcTime: 30 * 60 * 1000,
  });
}

// FUT-023 Phase 1 — ephemeral PR-scoped registries (FE API hooks).
//
// Fronts the three admin BFF routes:
//   GET  /api/v1/pr-registry/config     → usePRRegistryConfig
//   PUT  /api/v1/pr-registry/config     → useUpdatePRRegistryConfig
//   GET  /api/v1/pr-registry/namespaces → usePRNamespaces
//
// The webhook secret is write-only end-to-end: the GET only ever returns a
// `has_secret` boolean, and the PUT treats an empty `webhook_secret` as "keep
// the stored value" (mirrors the notification-webhook panel convention). The
// unauthenticated GitHub receiver (POST /webhooks/scm/github/pr) has no FE hook
// — GitHub calls it directly.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "./client";

// PRRegistryConfig is the wire shape of GET /pr-registry/config. The raw
// webhook secret is never present — only has_secret. webhook_url is the derived
// public receiver URL the admin pastes into GitHub (empty when PUBLIC_BASE_URL
// is unset on the BFF).
export interface PRRegistryConfig {
  enabled: boolean;
  has_secret: boolean;
  promote_target_org: string;
  webhook_url: string;
  updated_at?: string;
}

// PRRegistryConfigPut is the PUT body. An empty webhook_secret keeps the sealed
// value; a non-empty value re-seals it.
export interface PRRegistryConfigPut {
  enabled: boolean;
  webhook_secret: string; // empty = keep existing
  promote_target_org: string;
}

// PRNamespace is one row of the PR-namespace inventory.
export interface PRNamespace {
  provider: string;
  source_repo: string;
  pr_number: number;
  org_name: string;
  status: string;
  created_at?: string;
  torn_down_at?: string;
}

// PRNamespacesResponse is the envelope for GET /pr-registry/namespaces. The
// slice is always an array (the BFF pre-allocates it) so the FE never guards a
// null.
export interface PRNamespacesResponse {
  namespaces: PRNamespace[];
  next_page_token: string;
}

// PRNamespaceStatus narrows the status query param the list route accepts.
export type PRNamespaceStatus = "active" | "torn_down" | "all";

export const prRegistryKeys = {
  config: ["pr-registry-config"] as const,
  namespaces: (status: PRNamespaceStatus) =>
    ["pr-registry-namespaces", status] as const,
};

// usePRRegistryConfig fetches the current PR-registry config. Admin-only route;
// the panel that consumes it is itself admin-gated so this never fires for a
// non-admin.
export function usePRRegistryConfig() {
  return useQuery({
    queryKey: prRegistryKeys.config,
    queryFn: async () => {
      const { data } = await apiClient.get<PRRegistryConfig>(
        "/pr-registry/config",
      );
      return data;
    },
    staleTime: 30_000,
  });
}

// useUpdatePRRegistryConfig upserts the config. On success it primes the config
// cache with the server's canonical response so the panel re-seeds from it
// (clearing the write-only secret input + refreshing has_secret).
export function useUpdatePRRegistryConfig() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: PRRegistryConfigPut) => {
      const { data } = await apiClient.put<PRRegistryConfig>(
        "/pr-registry/config",
        body,
      );
      return data;
    },
    onSuccess: (data) => {
      qc.setQueryData(prRegistryKeys.config, data);
    },
  });
}

// usePRNamespaces lists PR-scoped registry namespaces. Defaults to the active
// set — the read-only inventory table the admin sees. page_size is maxed (100)
// so Phase 1 renders a single page; a "load more" is a cheap follow-up if the
// active count ever exceeds that.
export function usePRNamespaces(status: PRNamespaceStatus = "active") {
  return useQuery({
    queryKey: prRegistryKeys.namespaces(status),
    queryFn: async () => {
      const { data } = await apiClient.get<PRNamespacesResponse>(
        "/pr-registry/namespaces",
        { params: { status, page_size: 100 } },
      );
      return data;
    },
    staleTime: 15_000,
  });
}

import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { apiClient } from "./client";
import { workspaceKeys } from "./workspace";

// FE-API-027 — workspace custom domains.
//
// Five HTTP routes on the BFF backed by services/tenant:
//   GET    /api/v1/workspace/me/domains
//   POST   /api/v1/workspace/me/domains                       → returns TXT challenge
//   POST   /api/v1/workspace/me/domains/{domain}/verify       → force a poll
//   PATCH  /api/v1/workspace/me/domains/{domain}              → set primary
//   DELETE /api/v1/workspace/me/domains/{domain}
//
// Auth: tenant admin/owner on any org grant (BFF enforces; we don't gate
// client-side). After every mutation we invalidate both the domains list
// AND the workspace key — because promoting / verifying / deleting a
// domain changes `workspace.host` which the sidebar + pull command read.

export interface DomainEntry {
  domain: string;
  verified: boolean;
  is_primary: boolean;
  registered_at: string;
  verified_at?: string | null;
  next_poll_after?: string | null;
  notified_24h: boolean;
  notified_48h: boolean;
}

export interface DomainsListResponse {
  domains: DomainEntry[];
}

export interface RegisterDomainResponse {
  domain: string;
  verification_token: string;
  txt_record_name: string;
  instructions: string;
}

export const domainKeys = {
  all: ["domains"] as const,
  list: () => [...domainKeys.all, "list"] as const,
};

export function useDomains() {
  return useQuery({
    queryKey: domainKeys.list(),
    queryFn: async () => {
      const { data } = await apiClient.get<DomainsListResponse>(
        "/workspace/me/domains",
      );
      return data.domains;
    },
    staleTime: 30_000,
  });
}

function invalidateDomainsAndWorkspace(qc: ReturnType<typeof useQueryClient>) {
  // Every domain mutation can change which domain is primary, and the
  // primary domain becomes workspace.host — refresh both caches so the
  // sidebar header and PullCommandCard reflect the change immediately.
  void qc.invalidateQueries({ queryKey: domainKeys.list() });
  void qc.invalidateQueries({ queryKey: workspaceKeys.all });
}

export function useRegisterDomain() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (domain: string) => {
      const { data } = await apiClient.post<RegisterDomainResponse>(
        "/workspace/me/domains",
        { domain },
      );
      return data;
    },
    onSuccess: () => invalidateDomainsAndWorkspace(qc),
  });
}

export function useVerifyDomain() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (domain: string) => {
      const { data } = await apiClient.post<DomainEntry>(
        `/workspace/me/domains/${encodeURIComponent(domain)}/verify`,
      );
      return data;
    },
    onSuccess: () => invalidateDomainsAndWorkspace(qc),
  });
}

export function usePromoteDomain() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (domain: string) => {
      const { data } = await apiClient.patch<DomainEntry>(
        `/workspace/me/domains/${encodeURIComponent(domain)}`,
        { is_primary: true },
      );
      return data;
    },
    onSuccess: () => invalidateDomainsAndWorkspace(qc),
  });
}

export function useDeleteDomain() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (domain: string) => {
      await apiClient.delete(
        `/workspace/me/domains/${encodeURIComponent(domain)}`,
      );
    },
    onSuccess: () => invalidateDomainsAndWorkspace(qc),
  });
}

// Domain regex — RFC1035-style label rules, total length ≤253. Mirrors the
// backend's `reDomain` so the form catches typos before round-tripping.
export const DOMAIN_REGEX = /^(?=.{1,253}$)([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/;

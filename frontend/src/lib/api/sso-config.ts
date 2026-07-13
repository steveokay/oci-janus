// FE-API-034 — SSO admin config (FE API hooks).
//
// Fronts the three global-admin BFF routes on registry-auth (reachable via the
// FE proxy at /api/v1/auth/admin/providers):
//   GET    /api/v1/auth/admin/providers        → useSSOProviders  (list)
//   PUT    /api/v1/auth/admin/providers/{id}    → useUpsertSSOProvider
//   DELETE /api/v1/auth/admin/providers/{id}    → useDeleteSSOProvider
//
// The OAuth client secret is write-only end-to-end: the GET only ever returns a
// `has_secret` boolean, and the PUT treats an empty `client_secret` as "keep the
// stored value" (mirrors the notification-webhook + pr-registry panels). On a
// CREATE (new id) an empty secret is a 400 from the backend.
//
// SSO kinds are OAuth-only in v1 — the backend rejects `saml` with a 400, so the
// FE never offers it.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "./client";

// AdminProvider is the wire shape of a configured SSO provider as returned by
// the list + upsert routes. The raw client secret is never present — only the
// has_secret flag telling the FE whether one is stored.
export interface AdminProvider {
  id: string;
  kind: string;
  display_name: string;
  enabled: boolean;
  oauth_client_id: string;
  oauth_issuer_url: string;
  oauth_scopes: string[];
  has_secret: boolean;
  auto_provision: boolean;
  created_at: string;
  updated_at: string;
}

// AdminProvidersResponse is the envelope of GET /auth/admin/providers. The
// slice is always an array (the backend pre-allocates it) so the FE never
// guards a null.
export interface AdminProvidersResponse {
  providers: AdminProvider[];
}

// PutBody is the PUT request body. An empty client_secret keeps the stored
// value on an existing provider; a non-empty value re-seals it. On CREATE the
// backend requires a non-empty secret (400 otherwise).
export interface PutBody {
  kind: string;
  display_name: string;
  enabled: boolean;
  oauth_client_id: string;
  oauth_issuer_url: string;
  oauth_scopes: string[];
  client_secret: string; // empty = keep existing (existing providers only)
  auto_provision: boolean;
}

export const ssoConfigKeys = { all: ["sso-config-providers"] as const };

// useSSOProviders fetches the configured SSO providers. Global-admin-only
// route; the panel that consumes it is itself admin-gated so this never fires
// for a non-admin.
export function useSSOProviders() {
  return useQuery({
    queryKey: ssoConfigKeys.all,
    queryFn: async () => {
      const { data } = await apiClient.get<AdminProvidersResponse>(
        "/auth/admin/providers",
      );
      return data.providers;
    },
    staleTime: 30_000,
  });
}

// useUpsertSSOProvider creates or updates a provider by id. On success it
// invalidates the list so the table re-fetches the canonical rows (clearing the
// write-only secret input + refreshing has_secret).
export function useUpsertSSOProvider() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, body }: { id: string; body: PutBody }) => {
      const { data } = await apiClient.put<AdminProvider>(
        `/auth/admin/providers/${id}`,
        body,
      );
      return data;
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ssoConfigKeys.all });
    },
  });
}

// useDeleteSSOProvider removes a provider by id (204 on success, 404 if it was
// already gone). Invalidates the list on success.
export function useDeleteSSOProvider() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => {
      await apiClient.delete(`/auth/admin/providers/${id}`);
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ssoConfigKeys.all });
    },
  });
}

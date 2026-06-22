import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { apiClient } from "./client";

// Service-account hooks (FE-API-048 T23).
//
// All routes proxy through /api/v1/ (Vite dev) → localhost:8080 (registry-auth).
// The SA API is admin-only server-side; these hooks don't enforce that — the
// backend returns 403 for non-admin callers.

// ServiceAccount mirrors the serviceAccountResponse shape from
// services/auth/internal/handler/http_service_accounts.go.
// created_by and disabled_at are omitted (omitempty) by the backend when
// absent, so they are typed as optional here.
export interface ServiceAccount {
  id: string;
  tenant_id: string;
  name: string;
  description: string;
  allowed_scopes: string[];
  shadow_user_id: string;
  // created_by is absent when the creator's account has been deleted.
  created_by?: string | null;
  created_at: string;
  // disabled_at is absent while the account is active.
  disabled_at?: string | null;
  // active_key_count is populated on list responses; 0 on single-row responses.
  active_key_count: number;
  // last_used_at is not returned by the current backend (T14 enrichment does
  // not include it), but is defined here for forward-compatibility.
  last_used_at?: string | null;
}

// SAApiKey mirrors the saKeyResponse shape from http_service_accounts.go.
// The `key` field (raw secret) is only present immediately after creation.
export interface SAApiKey {
  id: string;
  name: string;
  prefix: string;
  scopes: string[];
  expires_at?: string | null;
  created_at: string;
  // key is the plaintext secret — shown exactly once on issue, absent on list.
  key?: string;
}

// Query key factory — keeps all SA-related keys under a single namespace so
// invalidation patterns stay consistent across hooks.
export const saKeys = {
  all: ["service-accounts"] as const,
  one: (id: string) => ["service-accounts", id] as const,
  keys: (id: string) => ["service-accounts", id, "api-keys"] as const,
};

// ── LIST /api/v1/service-accounts ────────────────────────────────────────────

interface ListSAResponse {
  service_accounts: ServiceAccount[];
  next_page_token?: string;
}

// useServiceAccounts fetches all service accounts for the caller's tenant.
// Pass includeDisabled: true to include disabled accounts (backend default:
// active only).
export function useServiceAccounts(opts?: { includeDisabled?: boolean }) {
  return useQuery({
    queryKey: saKeys.all,
    queryFn: async () => {
      const { data } = await apiClient.get<ListSAResponse>(
        "/service-accounts",
        {
          params: opts?.includeDisabled
            ? { include_disabled: "true" }
            : undefined,
        },
      );
      return data.service_accounts;
    },
    staleTime: 20_000,
  });
}

// ── GET /api/v1/service-accounts/{id} ────────────────────────────────────────

// useServiceAccount fetches a single service account by ID.
export function useServiceAccount(id: string) {
  return useQuery({
    queryKey: saKeys.one(id),
    queryFn: async () => {
      const { data } = await apiClient.get<ServiceAccount>(
        `/service-accounts/${encodeURIComponent(id)}`,
      );
      return data;
    },
    enabled: !!id,
    staleTime: 20_000,
  });
}

// ── POST /api/v1/service-accounts ────────────────────────────────────────────

interface CreateServiceAccountBody {
  name: string;
  description?: string;
  allowed_scopes?: string[];
}

// useCreateServiceAccount creates a new service account. On success it
// invalidates the full list so the new entry appears immediately.
export function useCreateServiceAccount() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: CreateServiceAccountBody) => {
      const { data } = await apiClient.post<ServiceAccount>(
        "/service-accounts",
        body,
      );
      return data;
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: saKeys.all });
    },
  });
}

// ── PATCH /api/v1/service-accounts/{id} ──────────────────────────────────────

interface UpdateServiceAccountBody {
  id: string;
  name?: string;
  description?: string;
  allowed_scopes?: string[];
}

// useUpdateServiceAccount sends a partial PATCH to update name / description /
// allowed_scopes for a service account. Invalidates both the list and the
// individual-item cache so all consumers see fresh data.
export function useUpdateServiceAccount() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, ...body }: UpdateServiceAccountBody) => {
      const { data } = await apiClient.patch<ServiceAccount>(
        `/service-accounts/${encodeURIComponent(id)}`,
        body,
      );
      return data;
    },
    onSuccess: (_data, { id }) => {
      void qc.invalidateQueries({ queryKey: saKeys.all });
      void qc.invalidateQueries({ queryKey: saKeys.one(id) });
    },
  });
}

// ── PATCH /api/v1/service-accounts/{id} { disabled: true } ───────────────────

// useDisableServiceAccount toggles the disabled state of a service account.
// The handler uses a single PATCH endpoint shared with general field updates;
// the `disabled` field maps to SetDisabled internally.
export function useDisableServiceAccount() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({
      id,
      disabled,
    }: {
      id: string;
      disabled: boolean;
    }) => {
      const { data } = await apiClient.patch<ServiceAccount>(
        `/service-accounts/${encodeURIComponent(id)}`,
        { disabled },
      );
      return data;
    },
    onSuccess: (_data, { id }) => {
      void qc.invalidateQueries({ queryKey: saKeys.all });
      void qc.invalidateQueries({ queryKey: saKeys.one(id) });
    },
  });
}

// ── DELETE /api/v1/service-accounts/{id} ─────────────────────────────────────

// useDeleteServiceAccount hard-deletes a service account and its shadow user.
// Returns 204 No Content on success.
export function useDeleteServiceAccount() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => {
      await apiClient.delete(
        `/service-accounts/${encodeURIComponent(id)}`,
      );
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: saKeys.all });
    },
  });
}

// ── GET /api/v1/service-accounts/{id}/api-keys ───────────────────────────────

interface ListSAKeysResponse {
  api_keys: SAApiKey[];
}

// useSAKeys fetches all active API keys owned by a service account.
export function useSAKeys(saID: string) {
  return useQuery({
    queryKey: saKeys.keys(saID),
    queryFn: async () => {
      const { data } = await apiClient.get<ListSAKeysResponse>(
        `/service-accounts/${encodeURIComponent(saID)}/api-keys`,
      );
      return data.api_keys;
    },
    enabled: !!saID,
    staleTime: 20_000,
  });
}

// ── POST /api/v1/service-accounts/{id}/api-keys ──────────────────────────────

interface IssueSAKeyBody {
  name: string;
  scopes: string[];
}

// useIssueSAKey creates a new API key owned by the service account.
// The raw secret is returned in the `key` field and shown exactly once.
// Invalidates both the key list and the SA list so active_key_count refreshes.
export function useIssueSAKey(saID: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: IssueSAKeyBody) => {
      const { data } = await apiClient.post<SAApiKey>(
        `/service-accounts/${encodeURIComponent(saID)}/api-keys`,
        body,
      );
      return data;
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: saKeys.keys(saID) });
      // Invalidate the full list so active_key_count updates for this SA row.
      void qc.invalidateQueries({ queryKey: saKeys.all });
    },
  });
}

// ── DELETE /api/v1/service-accounts/{id}/api-keys/{keyId} ────────────────────

// useRevokeSAKey revokes (soft-deletes) a single API key owned by a service
// account. Returns 204 No Content on success.
// Invalidates both the key list and the SA list so active_key_count refreshes.
export function useRevokeSAKey(saID: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (keyId: string) => {
      await apiClient.delete(
        `/service-accounts/${encodeURIComponent(saID)}/api-keys/${encodeURIComponent(keyId)}`,
      );
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: saKeys.keys(saID) });
      // Invalidate the full list so active_key_count updates for this SA row.
      void qc.invalidateQueries({ queryKey: saKeys.all });
    },
  });
}

// ── POST /api/v1/service-accounts/{id}/scopes/preflight ──────────────────────

// useScopeShrinkPreflight queries how many active keys would be affected if
// the SA's AllowedScopes were narrowed to the provided list. Callers display
// the count to the operator before committing the scope change so the impact
// is visible before it takes effect.
export function useScopeShrinkPreflight() {
  return useMutation({
    mutationFn: async (args: { saID: string; allowedScopes: string[] }) => {
      const { data } = await apiClient.post<{ affected_keys: number }>(
        `/service-accounts/${encodeURIComponent(args.saID)}/scopes/preflight`,
        { allowed_scopes: args.allowedScopes },
      );
      return data.affected_keys;
    },
  });
}

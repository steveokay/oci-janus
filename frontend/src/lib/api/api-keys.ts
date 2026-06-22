import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { apiClient } from "./client";

// Beacon — API key hooks.
//
// Wire shape comes from `services/auth/internal/handler/http.go`
// `apiKeyResponse`: { id, name, prefix, scopes, expires_at?, created_at,
// last_used_at?, key? }. `key` is populated only on creation (raw secret,
// shown once); on list it's omitted. POST returns the plaintext key once;
// the SecretRevealDialog (Sprint 5) renders it for the operator to copy.
//
// B2 fix (sprint-11 maint batch 1): the previous types used `key_id` /
// `secret` / `ListResponse{ keys: [] }` / `description` / `last_used_at`
// — none of which match what services/auth actually emits. The list call
// used to crash with `.slice()` of undefined on every row.

export interface ApiKey {
  id: string;
  name: string;
  prefix: string;
  scopes: string[];
  expires_at?: string;
  created_at: string;
  last_used_at?: string | null;
}

export interface CreatedApiKey extends ApiKey {
  // The raw secret. Backend field name is `key`; shown exactly once.
  key: string;
}

export const apiKeyKeys = {
  all: ["api-keys"] as const,
};

export function useApiKeys() {
  return useQuery({
    queryKey: apiKeyKeys.all,
    queryFn: async () => {
      // /apikeys returns an array directly (not wrapped in { keys: [...] }).
      const { data } = await apiClient.get<ApiKey[]>("/apikeys");
      return data;
    },
    staleTime: 20_000,
  });
}

interface CreateBody {
  name: string;
  scopes?: string[];
  expires_at?: string;
}

export function useCreateApiKey() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: CreateBody) => {
      const { data } = await apiClient.post<CreatedApiKey>("/apikeys", body);
      return data;
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: apiKeyKeys.all });
    },
  });
}

export function useDeleteApiKey() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (keyId: string) => {
      await apiClient.delete(`/apikeys/${encodeURIComponent(keyId)}`);
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: apiKeyKeys.all });
    },
  });
}

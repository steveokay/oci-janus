import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { apiClient } from "./client";

// Beacon — API key hooks.
//
// POST returns the plaintext `secret` exactly once. We never persist it on
// the client; the SecretRevealDialog (Sprint 5) renders it for the operator
// to copy + acknowledge, then it falls out of memory.

export interface ApiKey {
  key_id: string;
  name: string;
  description?: string;
  created_at: string;
  last_used_at: string | null;
}

interface ListResponse {
  keys: ApiKey[];
}

export interface CreatedApiKey extends ApiKey {
  secret: string;
}

export const apiKeyKeys = {
  all: ["api-keys"] as const,
};

export function useApiKeys() {
  return useQuery({
    queryKey: apiKeyKeys.all,
    queryFn: async () => {
      const { data } = await apiClient.get<ListResponse>("/apikeys");
      return data.keys;
    },
    staleTime: 20_000,
  });
}

interface CreateBody {
  name: string;
  description?: string;
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

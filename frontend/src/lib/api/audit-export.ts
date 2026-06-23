import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "./client";
import type {
  AuditExportConfig,
  AuditExportConfigResponse,
  AuditExportFormat,
  AuditExportTestResponse,
} from "./types";

// TanStack Query hooks for futures.md Tier 1 #4 — audit log
// streaming to SIEM. Separate module from workspace.ts because the
// CRUD operations target their own route prefix and the cache key
// space stays narrow + invalidatable as a unit.

export const auditExportKeys = {
  all: ["audit-export"] as const,
  config: () => [...auditExportKeys.all, "config"] as const,
};

export function useAuditExportConfig() {
  return useQuery({
    queryKey: auditExportKeys.config(),
    queryFn: async (): Promise<AuditExportConfig | null> => {
      const { data } = await apiClient.get<AuditExportConfigResponse>(
        "/workspace/me/audit-export",
      );
      return data.config;
    },
  });
}

export interface AuditExportPutArgs {
  enabled: boolean;
  format: AuditExportFormat;
  target_url: string;
  // hmac_secret + bearer_token are the PLAINTEXT secret. Send the
  // empty string to leave the existing column alone; set `*_clear`
  // to revoke. The BFF + audit service apply AES-256-GCM seal
  // server-side; the secret never lives in localStorage / Zustand.
  hmac_secret?: string;
  hmac_secret_clear?: boolean;
  bearer_token?: string;
  bearer_token_clear?: boolean;
  // event_filters_json must parse as JSON if provided. Shape:
  //   {"include":["push.completed"], "exclude":["webhook.*"]}
  // Empty / undefined falls back to "send everything."
  event_filters_json?: string;
}

export function useUpdateAuditExportConfig() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: AuditExportPutArgs): Promise<AuditExportConfig> => {
      const { data } = await apiClient.put<AuditExportConfigResponse>(
        "/workspace/me/audit-export",
        body,
      );
      // PUT response always has a non-null config — the server only
      // returns null on GET when the row genuinely doesn't exist.
      // Force-cast here so the caller doesn't have to handle the
      // theoretical null.
      return data.config as AuditExportConfig;
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: auditExportKeys.config() });
    },
  });
}

export function useDeleteAuditExportConfig() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (): Promise<void> => {
      await apiClient.delete("/workspace/me/audit-export");
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: auditExportKeys.config() });
    },
  });
}

export function useTestAuditExportConfig() {
  return useMutation({
    mutationFn: async (): Promise<AuditExportTestResponse> => {
      const { data } = await apiClient.post<AuditExportTestResponse>(
        "/workspace/me/audit-export/test",
      );
      return data;
    },
  });
}

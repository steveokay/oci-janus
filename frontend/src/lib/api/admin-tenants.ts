import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { apiClient } from "./client";

// Beacon — platform-admin tenant hooks (S6).
//
// All routes require the caller to hold the platform-admin marker grant
// (admin, org, "*"). The BFF returns 403 for under-privileged callers and
// 404 when TENANT_GRPC_ADDR is not configured on the BFF.
//
// Endpoints:
//   GET    /api/v1/admin/tenants
//   POST   /api/v1/admin/tenants
//   GET    /api/v1/admin/tenants/{tenantID}
//   DELETE /api/v1/admin/tenants/{tenantID}
//   PUT    /api/v1/admin/tenants/{tenantID}/quota

// ── Types ───────────────────────────────────────────────────────────────────

export interface AdminTenant {
  tenant_id: string;
  name: string;
  plan: string;
  created_at: string;
}

interface ListTenantsResponse {
  tenants: AdminTenant[];
  next_page_token?: string;
}

export interface SetQuotaResponse {
  tenant_id: string;
  used_bytes: number;
  quota_bytes: number;
}

// ── Key factory ─────────────────────────────────────────────────────────────

export const adminTenantKeys = {
  all: ["admin", "tenants"] as const,
  list: () => [...adminTenantKeys.all, "list"] as const,
  detail: (id: string) => [...adminTenantKeys.all, "detail", id] as const,
};

// ── Hooks ───────────────────────────────────────────────────────────────────

export function useAdminTenants() {
  return useQuery({
    queryKey: adminTenantKeys.list(),
    queryFn: async () => {
      const { data } = await apiClient.get<ListTenantsResponse>(
        "/admin/tenants",
      );
      return data.tenants;
    },
    staleTime: 30_000,
  });
}

interface CreateTenantBody {
  name: string;
  plan: string;
}

export function useCreateTenant() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: CreateTenantBody) => {
      const { data } = await apiClient.post<AdminTenant>(
        "/admin/tenants",
        body,
      );
      return data;
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: adminTenantKeys.list() });
    },
  });
}

export function useDeleteTenant() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (tenantId: string) => {
      await apiClient.delete(
        `/admin/tenants/${encodeURIComponent(tenantId)}`,
      );
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: adminTenantKeys.list() });
    },
  });
}

interface SetQuotaBody {
  tenantId: string;
  quotaBytes: number;
}

export function useSetTenantQuota() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ tenantId, quotaBytes }: SetQuotaBody) => {
      const { data } = await apiClient.put<SetQuotaResponse>(
        `/admin/tenants/${encodeURIComponent(tenantId)}/quota`,
        { quota_bytes: quotaBytes },
      );
      return data;
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: adminTenantKeys.list() });
    },
  });
}

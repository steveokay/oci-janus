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

// FE-API-028 — strict superset of AdminTenant with composed usage stats.
// `last_push_at` is null when the tenant has never recorded a push (BFF
// guarantees this is null rather than Unix epoch).
export interface AdminTenantDetail extends AdminTenant {
  slug: string;
  host: string;
  host_is_custom: boolean;
  storage_used_bytes: number;
  storage_quota_bytes: number;
  repository_count: number;
  organization_count: number;
  user_count: number;
  last_push_at: string | null;
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

// FE-API-029 — at least one of name / plan must be set.
export interface UpdateTenantBody {
  name?: string;
  plan?: string;
}

export const TENANT_PLANS = ["free", "pro", "enterprise"] as const;
export type TenantPlan = (typeof TENANT_PLANS)[number];
export const TENANT_NAME_REGEX = /^[a-z0-9][a-z0-9-]{1,63}$/;

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
    onSuccess: (_, vars) => {
      void qc.invalidateQueries({ queryKey: adminTenantKeys.list() });
      void qc.invalidateQueries({
        queryKey: adminTenantKeys.detail(vars.tenantId),
      });
    },
  });
}

// FE-API-028 — composed tenant detail (storage + repo / org / user
// counts + last push). Enabled flag lets the drawer mount-and-disable
// while waiting for the row click to set the active tenant.
export function useAdminTenantDetail(tenantId: string | undefined) {
  return useQuery({
    queryKey: adminTenantKeys.detail(tenantId ?? ""),
    enabled: Boolean(tenantId),
    queryFn: async () => {
      const { data } = await apiClient.get<AdminTenantDetail>(
        `/admin/tenants/${encodeURIComponent(tenantId as string)}`,
      );
      return data;
    },
    staleTime: 30_000,
  });
}

// FE-API-029 — rename + plan change. On success we invalidate both the
// list (PlanBadge / name column may have shifted) and the detail key for
// this specific tenant.
export function useUpdateTenant() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({
      tenantId,
      body,
    }: {
      tenantId: string;
      body: UpdateTenantBody;
    }) => {
      const { data } = await apiClient.patch<AdminTenantDetail>(
        `/admin/tenants/${encodeURIComponent(tenantId)}`,
        body,
      );
      return data;
    },
    onSuccess: (_, vars) => {
      void qc.invalidateQueries({ queryKey: adminTenantKeys.list() });
      void qc.invalidateQueries({
        queryKey: adminTenantKeys.detail(vars.tenantId),
      });
    },
  });
}

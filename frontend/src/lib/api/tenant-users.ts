import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "./client";

// FUT-012 Phase C — frontend hooks for the tenant-user lifecycle.
//
// Backend: services/auth gRPC (Phase A) wrapped by services/management
// REST routes (Phase B). All five routes are gated on tenant-admin OR
// platform-admin marker — the FE doesn't pre-validate; a 403 surfaces
// in the toast if the caller lacks the grant.

export type TenantUserStatus = "active" | "invited" | "disabled";
export type TenantUserKind = "human" | "service_account";

export interface TenantUserRoleSummary {
  org_admin_count: number;
  org_writer_count: number;
  org_reader_count: number;
  repo_grant_count: number;
  tenant_admin: boolean;
  platform_admin: boolean;
}

export interface TenantUser {
  user_id: string;
  username: string;
  display_name: string;
  email: string;
  kind: TenantUserKind;
  status: TenantUserStatus;
  // ISO timestamp; absent (undefined) when the user has never logged in.
  last_login_at?: string;
  created_at: string;
  roles: TenantUserRoleSummary;
}

export interface TenantUsersPage {
  users: TenantUser[];
  next_page_token?: string;
  total_count: number;
}

interface ListParams {
  page_size?: number;
  page_token?: string;
}

export const tenantUserKeys = {
  all: ["tenant-users"] as const,
  list: (params: ListParams) =>
    [...tenantUserKeys.all, "list", params] as const,
};

export function useTenantUsers(params: ListParams = {}) {
  return useQuery({
    queryKey: tenantUserKeys.list(params),
    queryFn: async () => {
      const q: Record<string, string> = {};
      if (params.page_size) q.page_size = String(params.page_size);
      if (params.page_token) q.page_token = params.page_token;
      const { data } = await apiClient.get<TenantUsersPage>(
        "/tenant/users",
        { params: q },
      );
      return data;
    },
    staleTime: 15_000,
  });
}

// ── Invite ────────────────────────────────────────────────────────────

export interface InviteUserBody {
  email: string;
  display_name: string;
  // Both optional, but paired — the BFF rejects half-set requests.
  initial_org_role?: string;
  initial_org_name?: string;
  // Optional override; backend clamps to 30 days max + defaults to 7 days.
  expires_in_secs?: number;
}

export interface InviteUserResult {
  user_id: string;
  // Raw single-use token. Shown to the operator ONCE in the invite
  // dialog's reveal step; never persisted to localStorage or written
  // anywhere else. Same discipline as the api-key creation flow.
  invite_token: string;
  invite_expires_at: string;
}

export function useInviteUser() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: InviteUserBody): Promise<InviteUserResult> => {
      const { data } = await apiClient.post<InviteUserResult>(
        "/tenant/users/invite",
        body,
      );
      return data;
    },
    onSuccess: () => {
      // Invalidate ALL tenant-users queries — page-size variants share
      // a key prefix so this catches every cached page in the table.
      void qc.invalidateQueries({ queryKey: tenantUserKeys.all });
    },
  });
}

// ── Disable / Enable ──────────────────────────────────────────────────

export interface SetUserDisabledResult {
  // 'active' | 'disabled' — the resulting state after the flip.
  status: TenantUserStatus;
}

interface DisableArgs {
  user_id: string;
  // When true, POST /disable (sets disabled=true). When false, DELETE
  // /disable (clears disabled). Splits the destructive vs reversible
  // intent into two HTTP methods so the route audit log reads cleanly.
  disabled: boolean;
}

export function useSetUserDisabled() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ user_id, disabled }: DisableArgs): Promise<SetUserDisabledResult> => {
      const path = `/tenant/users/${encodeURIComponent(user_id)}/disable`;
      if (disabled) {
        const { data } = await apiClient.post<SetUserDisabledResult>(path);
        return data;
      }
      const { data } = await apiClient.delete<SetUserDisabledResult>(path);
      return data;
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: tenantUserKeys.all });
    },
  });
}

// ── Elevate to org-admin ──────────────────────────────────────────────

interface ElevateArgs {
  user_id: string;
  org: string;
}

export function useElevateToOrgAdmin() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ user_id, org }: ElevateArgs): Promise<void> => {
      await apiClient.post(
        `/tenant/users/${encodeURIComponent(user_id)}/elevate/${encodeURIComponent(org)}`,
      );
    },
    onSuccess: () => {
      // Org grants change role aggregates — re-fetch the table so the
      // row's chip strip reflects the new admin count.
      void qc.invalidateQueries({ queryKey: tenantUserKeys.all });
    },
  });
}

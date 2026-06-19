import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "./client";

// Beacon — RBAC membership hooks.
//
// Two parallel surfaces in the BFF: org-scoped grants and repo-scoped grants.
// We expose six hooks (3 per scope) sharing one Member type so the table /
// dialogs are reusable across both surfaces.

export type Role = "owner" | "admin" | "writer" | "reader";

export const ROLES: Role[] = ["owner", "admin", "writer", "reader"];

export interface Member {
  id: string;          // assignment UUID — used as the revoke key
  user_id: string;
  role: Role;
  granted_by: string;
}

interface MembersResponse {
  members: Member[];
}

interface GrantBody {
  user_id: string;
  role: Role;
}

// ── Key factory ─────────────────────────────────────────────────────────────

export const memberKeys = {
  all: ["members"] as const,
  org: (org: string) => [...memberKeys.all, "org", org] as const,
  repo: (org: string, repo: string) =>
    [...memberKeys.all, "repo", org, repo] as const,
};

// ── Org scope ───────────────────────────────────────────────────────────────

export function useOrgMembers(org: string) {
  return useQuery({
    queryKey: memberKeys.org(org),
    queryFn: async () => {
      const { data } = await apiClient.get<MembersResponse>(
        `/orgs/${encodeURIComponent(org)}/members`,
      );
      return data.members;
    },
    staleTime: 20_000,
    enabled: Boolean(org),
  });
}

export function useGrantOrgRole(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: GrantBody) => {
      const { data } = await apiClient.post<Member>(
        `/orgs/${encodeURIComponent(org)}/members`,
        body,
      );
      return data;
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: memberKeys.org(org) });
    },
  });
}

export function useRevokeOrgRole(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (assignmentId: string) => {
      await apiClient.delete(
        `/orgs/${encodeURIComponent(org)}/members/${encodeURIComponent(assignmentId)}`,
      );
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: memberKeys.org(org) });
    },
  });
}

// ── Repo scope ──────────────────────────────────────────────────────────────

export function useRepoMembers(org: string, repo: string) {
  return useQuery({
    queryKey: memberKeys.repo(org, repo),
    queryFn: async () => {
      const { data } = await apiClient.get<MembersResponse>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/members`,
      );
      return data.members;
    },
    staleTime: 20_000,
    enabled: Boolean(org && repo),
  });
}

export function useGrantRepoRole(org: string, repo: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: GrantBody) => {
      const { data } = await apiClient.post<Member>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/members`,
        body,
      );
      return data;
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: memberKeys.repo(org, repo) });
    },
  });
}

export function useRevokeRepoRole(org: string, repo: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (assignmentId: string) => {
      await apiClient.delete(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/members/${encodeURIComponent(assignmentId)}`,
      );
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: memberKeys.repo(org, repo) });
    },
  });
}

// UUID validation — used by the AddMember dialog. The grant endpoint takes
// a user_id (not a username) since there's no user-search endpoint yet
// (FE-API-future). Until then we accept UUIDs directly with clear hint copy.
export const UUID_REGEX =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

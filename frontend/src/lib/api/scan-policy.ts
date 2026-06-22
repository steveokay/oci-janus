import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { apiClient } from "./client";

// Beacon — Scan policies CRUD (FE-API-018).
//
// Backend routes:
//   GET /api/v1/security/policies   — any authenticated tenant user
//   PUT /api/v1/security/policies   — admin/owner on any org in the tenant
//
// GET returns the active policy (or a synthetic default if none set —
// the backend never 404s for "no row yet"). PUT is a full replace; PATCH
// is not supported.
//
// The BFF returns 404 ("route disabled") when SCANNER_GRPC_ADDR is not
// configured. We surface this to the editor so a control plane without
// scanner wiring shows a friendly "scanner not wired" empty state instead
// of a generic error.

// ── Types ───────────────────────────────────────────────────────────────────

// BlockOnSeverity uses "" (empty string) to mean "never block on push".
export type BlockOnSeverity = "" | "CRITICAL" | "HIGH" | "MEDIUM" | "LOW";

export type ScannerPlugin = "trivy" | "grype" | "clair";

export const BLOCK_SEVERITY_CHOICES: ReadonlyArray<{
  value: BlockOnSeverity;
  label: string;
  description: string;
}> = [
  {
    value: "",
    label: "Never block",
    description: "Pushes always succeed; scan findings are recorded but advisory only.",
  },
  {
    value: "CRITICAL",
    label: "Critical",
    description: "Block pushes whose scan reveals any CRITICAL CVE.",
  },
  {
    value: "HIGH",
    label: "High or critical",
    description: "Block pushes whose scan reveals HIGH or CRITICAL findings.",
  },
  {
    value: "MEDIUM",
    label: "Medium or above",
    description: "Block pushes whose scan reveals MEDIUM, HIGH, or CRITICAL.",
  },
  {
    value: "LOW",
    label: "Anything found",
    description: "Block pushes that surface any finding at any severity.",
  },
];

export const SCANNER_PLUGIN_CHOICES: ReadonlyArray<{
  value: ScannerPlugin;
  label: string;
  description: string;
}> = [
  {
    value: "trivy",
    label: "Trivy",
    description: "Aqua Security's open-source scanner. The default for new tenants.",
  },
  {
    value: "grype",
    label: "Grype",
    description: "Anchore's open-source scanner. Currently behind Trivy on CVE coverage.",
  },
  {
    value: "clair",
    label: "Clair",
    description: "Red Hat's open-source scanner. Needs --profile clair on the compose stack.",
  },
];

// CVE-ID validation — kept in sync with the BFF allowlist regex so the
// client can pre-validate before sending the request.
export const CVE_ID_REGEX = /^CVE-\d{4}-\d{4,7}$/;

export interface ScanPolicy {
  tenant_id: string;
  auto_scan_on_push: boolean;
  block_on_severity: BlockOnSeverity;
  exempt_cves: string[];
  scanner_plugin: ScannerPlugin;
  scanner_version_pin: string;
  updated_at?: string;
  updated_by?: string;
}

// PUT body shape — drops the server-managed fields. Naming must match the
// snake_case JSON on the wire.
export interface UpdateScanPolicyBody {
  auto_scan_on_push: boolean;
  block_on_severity: BlockOnSeverity;
  exempt_cves: string[];
  scanner_plugin: ScannerPlugin;
  scanner_version_pin: string;
}

// ── Key factory ─────────────────────────────────────────────────────────────

export const scanPolicyKeys = {
  all: ["scan-policy"] as const,
  current: () => [...scanPolicyKeys.all, "current"] as const,
};

// ── Hooks ───────────────────────────────────────────────────────────────────

// usePolicy — fetch the active policy. 30s staleTime mirrors the other
// "tenant config" surfaces (admin tenants, scanner adapters). The policy
// rarely changes so polling faster doesn't buy anything.
export function usePolicy() {
  return useQuery({
    queryKey: scanPolicyKeys.current(),
    queryFn: async () => {
      const { data } = await apiClient.get<ScanPolicy>("/security/policies");
      return data;
    },
    staleTime: 30_000,
  });
}

// useUpdatePolicy — replace the policy. On success, invalidate so the
// editor re-renders with the server's canonical values (notably the
// updated_at / updated_by stamps the BFF fills in).
export function useUpdatePolicy() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: UpdateScanPolicyBody) => {
      const { data } = await apiClient.put<ScanPolicy>(
        "/security/policies",
        body,
      );
      return data;
    },
    onSuccess: (fresh) => {
      // Seed the cache with the fresh server response immediately so the
      // form's "dirty" baseline shifts to the new saved state without an
      // extra round-trip. Then invalidate for safety in case another tab
      // also wrote.
      qc.setQueryData(scanPolicyKeys.current(), fresh);
      void qc.invalidateQueries({ queryKey: scanPolicyKeys.current() });
    },
  });
}

// ── FE-API-049 — org-default + per-repo scan policy ─────────────────────────

// ScopedScanPolicy extends the per-tenant shape with the new scope
// identifiers + enabled flag the FE-API-049 routes return. inherited_from
// is populated only on the per-repo GET when no per-repo override
// exists (the BFF resolves through the inheritance chain server-side).
export interface ScopedScanPolicy extends ScanPolicy {
  org_id?: string;
  repo_id?: string;
  enabled: boolean;
  inherited_from?: "repo" | "org" | "tenant" | "default" | "";
}

// UpdateScopedScanPolicyBody is the PUT shape for both scopes. Mirrors
// UpdateScanPolicyBody + the enabled toggle that scoped policies
// support (the per-tenant row treats enabled as implicit true).
export interface UpdateScopedScanPolicyBody extends UpdateScanPolicyBody {
  enabled: boolean;
}

// Cache key extensions. Keyed by scope identifier so the same hook
// shape works for many orgs / repos open in tabs.
export const orgScanPolicyKey = (org: string) =>
  [...scanPolicyKeys.all, "org", org] as const;
export const repoScanPolicyKey = (org: string, repo: string) =>
  [...scanPolicyKeys.all, "repo", org, repo] as const;

// Result shape — collapses the 404 "no policy yet" body into a notFound
// flag so the empty-state branch in the editor stays a boolean rather
// than a try/catch.
export interface ScopedScanPolicyResult {
  policy: ScopedScanPolicy | undefined;
  notFound: boolean;
  isLoading: boolean;
  isError: boolean;
  refetch: () => void;
}

// useOrgScanPolicy — GET the org-default. 404 with code "no-policy"
// collapses to notFound; everything else is an error.
export function useOrgScanPolicy(org: string): ScopedScanPolicyResult {
  const q = useQuery({
    queryKey: orgScanPolicyKey(org),
    queryFn: async () => {
      try {
        const { data } = await apiClient.get<ScopedScanPolicy>(
          `/orgs/${encodeURIComponent(org)}/policies/scan`,
        );
        return { policy: data, notFound: false } as const;
      } catch (err) {
        // The BFF returns 404 with { code: "no-policy" } when no row
        // exists. Treat that as a clean empty state.
        const ax = err as { response?: { status?: number; data?: { code?: string } } };
        if (
          ax.response?.status === 404 &&
          ax.response.data?.code === "no-policy"
        ) {
          return { policy: undefined, notFound: true } as const;
        }
        throw err;
      }
    },
    staleTime: 30_000,
  });
  return {
    policy: q.data?.policy,
    notFound: q.data?.notFound ?? false,
    isLoading: q.isLoading,
    isError: q.isError,
    refetch: () => void q.refetch(),
  };
}

// useUpdateOrgScanPolicy — PUT the org default. Invalidates every
// per-repo cache under this org so child surfaces flip from "inherited
// from tenant" to "inherited from org" on the next fetch.
export function useUpdateOrgScanPolicy(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: UpdateScopedScanPolicyBody) => {
      const { data } = await apiClient.put<ScopedScanPolicy>(
        `/orgs/${encodeURIComponent(org)}/policies/scan`,
        body,
      );
      return data;
    },
    onSuccess: (fresh) => {
      qc.setQueryData(orgScanPolicyKey(org), {
        policy: fresh,
        notFound: false,
      });
      void qc.invalidateQueries({ queryKey: orgScanPolicyKey(org) });
      // Bust every per-repo scan policy cache so children re-resolve.
      void qc.invalidateQueries({
        predicate: (q) =>
          q.queryKey[0] === "scan-policy" && q.queryKey[1] === "repo",
      });
    },
  });
}

// useDeleteOrgScanPolicy — DELETE. Returns 204 on success / 404 if no
// row existed. The mutation surfaces both as success on the FE side —
// the operator gets a "removed" toast either way (the BFF differentiates
// for audit purposes).
export function useDeleteOrgScanPolicy(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      await apiClient.delete(`/orgs/${encodeURIComponent(org)}/policies/scan`);
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: orgScanPolicyKey(org) });
      void qc.invalidateQueries({
        predicate: (q) =>
          q.queryKey[0] === "scan-policy" && q.queryKey[1] === "repo",
      });
    },
  });
}

// useRepoScanPolicy — GET the effective policy for one repo. Always
// returns SOME policy (the BFF's chain terminates in a synthesised
// default) — the inherited_from label distinguishes the source. notFound
// is wired here for parity with the org hook, but realistically only
// fires when SCANNER_GRPC_ADDR is unset (the route returns 404
// "route disabled").
export function useRepoScanPolicy(org: string, repo: string): ScopedScanPolicyResult {
  const q = useQuery({
    queryKey: repoScanPolicyKey(org, repo),
    queryFn: async () => {
      try {
        const { data } = await apiClient.get<ScopedScanPolicy>(
          `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/policies/scan`,
        );
        return { policy: data, notFound: false } as const;
      } catch (err) {
        const ax = err as { response?: { status?: number } };
        if (ax.response?.status === 404) {
          // "route disabled" — surface as empty so the FE renders a
          // friendly "scanner not wired" panel rather than a 500.
          return { policy: undefined, notFound: true } as const;
        }
        throw err;
      }
    },
    staleTime: 30_000,
  });
  return {
    policy: q.data?.policy,
    notFound: q.data?.notFound ?? false,
    isLoading: q.isLoading,
    isError: q.isError,
    refetch: () => void q.refetch(),
  };
}

// useUpdateRepoScanPolicy — PUT the per-repo override.
export function useUpdateRepoScanPolicy(org: string, repo: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: UpdateScopedScanPolicyBody) => {
      const { data } = await apiClient.put<ScopedScanPolicy>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/policies/scan`,
        body,
      );
      return data;
    },
    onSuccess: (fresh) => {
      qc.setQueryData(repoScanPolicyKey(org, repo), {
        policy: fresh,
        notFound: false,
      });
      void qc.invalidateQueries({ queryKey: repoScanPolicyKey(org, repo) });
    },
  });
}

// useDeleteRepoScanPolicy — DELETE the per-repo override. Repo falls
// back to org default (or tenant fallback) on the next push.
export function useDeleteRepoScanPolicy(org: string, repo: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      await apiClient.delete(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/policies/scan`,
      );
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: repoScanPolicyKey(org, repo) });
    },
  });
}

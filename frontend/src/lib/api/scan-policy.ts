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

export type ScannerPlugin = "trivy" | "grype";

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

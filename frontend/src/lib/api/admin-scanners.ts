import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { AxiosError } from "axios";
import { apiClient } from "./client";

// Beacon — platform-admin scanner adapter hooks (REM-011 Phase 2 / FE-API-044..047).
//
// All routes require the caller to hold the platform-admin marker grant
// (admin, org, "*"). The BFF returns 403 for under-privileged callers and
// 404 when SCANNER_GRPC_ADDR is not configured on the management BFF
// (treated as "no adapters installed" up in the UI rather than as an error).
//
// Endpoints:
//   GET   /api/v1/admin/scanners            — list installed adapters
//   GET   /api/v1/admin/scanners/active     — get active adapter
//   PATCH /api/v1/admin/scanners/active     — set active adapter (in-mem + DB)
//   POST  /api/v1/admin/scanners/test       — run a test scan via active adapter
//   GET   /api/v1/admin/scanners/health     — live worker pool liveness

// ── Types ───────────────────────────────────────────────────────────────────

// One installed scanner adapter binary discovered on disk by the scanner
// service. `env_keys` is read-only here: the UI surfaces it so operators
// can audit which env-vars the adapter will see at scan time, but mutation
// happens entirely through container env (compose / Helm) — not via the API.
export interface AdminAdapter {
  name: string;          // e.g. "trivy-adapter" or "dev-stub"
  version: string;       // e.g. "0.52.0" or "unknown"
  path: string;          // absolute path inside the scanner container
  checksum: string;      // SHA-256 hex digest of the binary
  size_bytes: number;
  env_keys: string[];    // env vars the adapter consumes
  active: boolean;       // exactly one adapter is `active` at any time
}

export interface AdminScannerListResponse {
  adapters: AdminAdapter[];
  active_adapter_path?: string;
}

// Live worker-pool liveness. `last_successful_scan_at` is absent (undefined)
// when no scan has ever succeeded on this adapter — render "Never" rather
// than the Unix epoch.
export interface AdminScannerHealth {
  healthy: boolean;
  last_successful_scan_at?: string;   // RFC3339; absent on "never"
  queue_depth: number;
  in_flight_count: number;
  active_adapter_name: string;
  active_adapter_version: string;
  // active_adapter_engine_reachable — liveness of the active adapter's
  // engine sidecar (e.g. trivy-engine). false means the sidecar itself is
  // down/unreachable — a deploy problem, distinct from `healthy=false`
  // (which reflects scan throughput). active_adapter_engine_detail carries
  // the reason and is only populated when reachable is false.
  active_adapter_engine_reachable: boolean;
  active_adapter_engine_detail?: string;
}

// Wire shape for the test-scan result. `ok=true` populates the scanner
// metadata + severity counts; `ok=false` populates `error_message` instead.
export interface AdminTestScanResult {
  ok: boolean;
  scanner_name?: string;
  scanner_version?: string;
  duration_ms: number;
  severity_counts?: Record<string, number>;
  error_message?: string;
}

interface SetActiveBody {
  adapter_path: string;
}

// ── Key factory ─────────────────────────────────────────────────────────────

export const adminScannerKeys = {
  all: ["admin", "scanners"] as const,
  list: () => [...adminScannerKeys.all, "list"] as const,
  active: () => [...adminScannerKeys.all, "active"] as const,
  health: () => [...adminScannerKeys.all, "health"] as const,
};

// ── Hooks ───────────────────────────────────────────────────────────────────

// useAdapters — list every adapter the scanner service has discovered.
// 30s staleTime matches the tenants list; the data only changes when an
// operator drops a new binary on disk + recreates the container, so polling
// faster doesn't buy anything.
export function useAdapters() {
  return useQuery({
    queryKey: adminScannerKeys.list(),
    queryFn: async () => {
      const { data } = await apiClient.get<AdminScannerListResponse>(
        "/admin/scanners",
      );
      return data;
    },
    staleTime: 30_000,
  });
}

// useActiveAdapter — separate fetch for the active adapter. The list already
// carries the `active` flag, so this is a thin convenience over `useAdapters`
// for surfaces that only care about which adapter is hot. We re-query the
// dedicated endpoint rather than deriving so the cache stays fresh even when
// somebody else swapped the active adapter from another tab.
export function useActiveAdapter() {
  return useQuery({
    queryKey: adminScannerKeys.active(),
    queryFn: async () => {
      const { data } = await apiClient.get<AdminAdapter>(
        "/admin/scanners/active",
      );
      return data;
    },
    staleTime: 30_000,
  });
}

// useSetActiveAdapter — flip the active adapter. The BFF performs an
// in-memory swap on the scanner worker pool AND persists the choice to
// `scanner_settings` in one call — no container restart required. We
// invalidate both list + active + health on success so the UI re-paints
// immediately with the new active row.
export function useSetActiveAdapter() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: SetActiveBody) => {
      const { data } = await apiClient.patch<AdminAdapter>(
        "/admin/scanners/active",
        body,
      );
      return data;
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: adminScannerKeys.list() });
      void qc.invalidateQueries({ queryKey: adminScannerKeys.active() });
      void qc.invalidateQueries({ queryKey: adminScannerKeys.health() });
    },
  });
}

// useTestScan — run a quick scan against the configured dev fixture
// (SCANNER_TEST_* env on the BFF — defaults to dev tenant + dev/alpine:latest)
// using the currently-active adapter. Not retried automatically; the UI
// surfaces the Retry CTA on failure.
export function useTestScan() {
  return useMutation({
    mutationFn: async () => {
      const { data } = await apiClient.post<AdminTestScanResult>(
        "/admin/scanners/test",
        {},
      );
      return data;
    },
  });
}

interface ScannerHealthOptions {
  // Override the auto-refresh cadence; pass `false` to disable polling
  // entirely. Default is 10s — fast enough that an operator watching the
  // page sees queue depth move, slow enough not to hammer the gRPC path.
  refetchInterval?: number | false;
  // Disable the query — used by the ScanPanel fallback so non-admins
  // don't even fire the request (avoids a 403 in the network tab).
  enabled?: boolean;
}

// useScannerHealth — live liveness signal. 403 (under-privileged caller)
// and 404 (SCANNER_GRPC_ADDR not wired on the BFF) both resolve to
// `undefined` rather than throwing, so callers like the ScanPanel can use
// `health?.healthy === false` as a gate without writing two error paths.
// Real network / 5xx errors still throw so the React-Query error UI fires
// on the admin page itself.
export function useScannerHealth(opts: ScannerHealthOptions = {}) {
  return useQuery({
    queryKey: adminScannerKeys.health(),
    enabled: opts.enabled ?? true,
    queryFn: async (): Promise<AdminScannerHealth | undefined> => {
      try {
        const { data } = await apiClient.get<AdminScannerHealth>(
          "/admin/scanners/health",
        );
        return data;
      } catch (e) {
        // 403 (non-admin) or 404 (admin routes off / BFF not wired) →
        // surface as "unknown" so the consumer falls back to its prior
        // heuristic. Everything else propagates.
        if (e instanceof AxiosError) {
          const s = e.response?.status;
          if (s === 403 || s === 404) return undefined;
        }
        throw e;
      }
    },
    refetchInterval: opts.refetchInterval ?? 10_000,
    // Keep the previous value visible while we refetch so the queue-depth
    // tile doesn't blank on every poll.
    staleTime: 5_000,
  });
}

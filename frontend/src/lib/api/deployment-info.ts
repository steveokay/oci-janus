import { useQuery } from "@tanstack/react-query";
import { apiClient } from "./client";

// Deployment info — build version for the Settings › Workspace posture card.
//
// Backend surface: GET /api/v1/deployment-info — public, unauthenticated.
// Historically this also carried a `deployment_mode` flag that gated the
// multi-tenant chrome (tenant switcher, plan badge, Platform tab). That
// posture was retired — single-tenant is the only mode now (ADR-0031,
// REDESIGN-001 Phase 9) — so the FE consumes only `version`.
//
// Cached aggressively — the value does not change during a session.

export interface DeploymentInfo {
  // version is the build-time version string injected via -ldflags.
  // "dev" in local development; semver tag (e.g. "v1.2.3") in CI builds.
  version: string;
}

async function fetchDeploymentInfo(): Promise<DeploymentInfo> {
  const { data } = await apiClient.get<DeploymentInfo>("/deployment-info");
  return data;
}

// useDeploymentInfo returns the cached deployment info (build version). A
// single query key is shared across the app so the global QueryClient makes
// one network call per session (refetch only on window focus or stale-time
// expiry). Long staleTime + gcTime because the value is effectively immutable
// for the session — it only changes when the operator redeploys.
export function useDeploymentInfo() {
  return useQuery({
    queryKey: ["deployment-info"],
    queryFn: fetchDeploymentInfo,
    staleTime: 60 * 60_000, // 1h — the value never changes during a session
    gcTime: 60 * 60_000,
    retry: 1, // public endpoint; one retry is enough if the BFF blips
  });
}

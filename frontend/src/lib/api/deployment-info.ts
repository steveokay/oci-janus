import { useQuery } from "@tanstack/react-query";
import { apiClient } from "./client";

// Beacon — deployment posture (REDESIGN-001 Phase 4.1).
//
// Backend surface: GET /api/v1/deployment-info — public, unauthenticated.
// Returns the deployment mode + version so the FE can decide which chrome
// to render (tenant switcher, plan badge, signup form, etc.).
//
// Cached aggressively — deployment posture does not change during a session.

export type DeploymentMode = "single" | "multi";

export interface DeploymentInfo {
  // deployment_mode controls which tenant chrome the FE renders.
  // "single" — one tenant per deployment; hide tenant switcher, plan badge.
  // "multi"  — multi-tenant capability enabled; render tenant chrome.
  deployment_mode: DeploymentMode;
  // version is the build-time version string injected via -ldflags.
  // "dev" in local development; semver tag (e.g. "v1.2.3") in CI builds.
  version: string;
}

async function fetchDeploymentInfo(): Promise<DeploymentInfo> {
  const { data } = await apiClient.get<DeploymentInfo>("/deployment-info");
  return data;
}

// useDeploymentInfo returns the cached deployment posture. Single query key
// shared across the app; the global QueryClient ensures one network call per
// session (refetch only on window focus or stale-time expiry).
//
// Long staleTime + gcTime because deployment posture is effectively
// immutable for the session — it only changes when the operator restarts
// the BFF with a different DEPLOYMENT_MODE env var.
export function useDeploymentInfo() {
  return useQuery({
    queryKey: ["deployment-info"],
    queryFn: fetchDeploymentInfo,
    staleTime: 60 * 60_000, // 1h — the value never changes during a session
    gcTime: 60 * 60_000,
    retry: 1, // public endpoint; one retry is enough if the BFF blips
  });
}

// isSingleMode is a typed predicate for the literal `"single"` mode string
// that's spreading across the codebase as the redesign phases land
// (Phase 2.4 sidebar, Phase 2.5 login + topbar UUID chip, more coming).
// Returns false when `info` is undefined (cold cache) — every call site
// defaults to multi-mode behaviour during cold load because that's the
// pre-redesign default and strictly safer than flashing single-mode UI
// before the value resolves.
//
// Use this instead of `info?.deployment_mode === "single"` so a future
// rename to e.g. `DeploymentMode.Single` only touches one file.
export function isSingleMode(info: DeploymentInfo | undefined): boolean {
  return info?.deployment_mode === "single";
}

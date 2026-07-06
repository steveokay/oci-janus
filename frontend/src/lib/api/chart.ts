import { useQuery } from "@tanstack/react-query";
import { AxiosError } from "axios";
import { apiClient } from "./client";

// Beacon — Helm chart detail (FUT-022).
//
// ChartMaintainer / ChartDependency / ChartMetadata mirror the BFF
// ChartResponse (services/management/internal/handler/chart.go +
// chartparse.go) — snake_case on the wire. The BFF resolves the tag ->
// manifest, reads the Helm config + content-layer blobs from registry-core,
// and parses Chart.yaml metadata + values.yaml. The two halves fail
// independently: `metadata` is null (with `metadata_error`) when the config
// blob is missing/unparseable; `values` is "" (with `values_error`) when the
// content layer can't be read.

export interface ChartMaintainer {
  name?: string;
  email?: string;
  url?: string;
}

export interface ChartDependency {
  name?: string;
  version?: string;
  repository?: string;
}

export interface ChartMetadata {
  name: string;
  version: string;
  app_version?: string;
  description?: string;
  api_version?: string;
  type?: string;
  kube_version?: string;
  home?: string;
  icon?: string;
  deprecated?: boolean;
  keywords?: string[];
  sources?: string[];
  maintainers?: ChartMaintainer[];
  dependencies?: ChartDependency[];
  annotations?: Record<string, string>;
}

export interface ChartResponse {
  // null when the config blob is missing/unparseable — metadata_error explains.
  metadata: ChartMetadata | null;
  metadata_error?: string;
  values: string;
  values_truncated: boolean;
  values_error?: string;
}

export const chartKeys = {
  all: ["chart"] as const,
  detail: (org: string, repo: string, tag: string) =>
    [...chartKeys.all, org, repo, tag] as const,
};

// useChart fetches the Helm chart detail for a tag. `enabled` should be gated
// by the caller so it only fires for Helm artifacts + when the Chart tab is
// active. A 404 (core client not wired — the BFF returns "route disabled")
// resolves to null so the caller renders an empty state instead of an error,
// mirroring useManifest's forgiving 404 contract.
export function useChart(
  org: string,
  repo: string,
  tag: string,
  enabled: boolean,
) {
  return useQuery({
    queryKey: chartKeys.detail(org, repo, tag),
    queryFn: async (): Promise<ChartResponse | null> => {
      try {
        const { data } = await apiClient.get<ChartResponse>(
          `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/tags/${encodeURIComponent(tag)}/chart`,
        );
        return data;
      } catch (e) {
        if (e instanceof AxiosError && e.response?.status === 404) {
          return null;
        }
        throw e;
      }
    },
    staleTime: 30_000,
    enabled: enabled && Boolean(org && repo && tag),
  });
}

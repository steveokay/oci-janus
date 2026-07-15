import { useQuery } from "@tanstack/react-query";
import { apiClient } from "./client";

// Image diff between two tags (Tier 2 #3).
//
// GET /api/v1/repositories/{org}/{repo}/compare?from={tagA}&to={tagB}
//
// The BFF assembles the diff from data it already fetches per tag (manifest,
// image config, SBOM, scan) — no new metadata RPC. Each section is
// self-describing: `available:false` + `reason` when the underlying data is
// missing (unscanned tag, no SBOM, core not wired), so the UI renders a
// per-section empty state without inferring from absent fields.

export interface CompareSide {
  tag: string;
  digest: string;
  size_bytes: number;
  is_index: boolean;
}

export interface LayerRef {
  digest: string;
  size: number;
  media_type?: string;
}

export interface LayerDiff {
  added: LayerRef[];
  removed: LayerRef[];
  common_count: number;
  size_delta_bytes: number;
}

export interface EnvChange {
  key: string;
  from: string;
  to: string;
}

export interface ConfigDiff {
  available: boolean;
  reason?: string;
  env: {
    added: string[];
    removed: string[];
    changed: EnvChange[];
  };
  cmd_changed: boolean;
  from_cmd?: string[];
  to_cmd?: string[];
  entrypoint_changed: boolean;
  from_entrypoint?: string[];
  to_entrypoint?: string[];
  exposed_ports_added: string[];
  exposed_ports_removed: string[];
  working_dir_from?: string;
  working_dir_to?: string;
  user_from?: string;
  user_to?: string;
}

export interface PkgRef {
  name: string;
  version?: string;
}

export interface PkgChange {
  name: string;
  from_version: string;
  to_version: string;
}

export interface PackageDiff {
  available: boolean;
  reason?: string;
  added: PkgRef[];
  removed: PkgRef[];
  changed: PkgChange[];
}

export interface VulnRef {
  cve: string;
  severity?: string;
  package?: string;
  version?: string;
  fixed_in?: string;
}

export interface VulnDiff {
  available: boolean;
  reason?: string;
  added: VulnRef[];
  removed: VulnRef[];
}

export interface ImageDiff {
  from: CompareSide;
  to: CompareSide;
  layers: LayerDiff;
  config: ConfigDiff;
  packages: PackageDiff;
  vulnerabilities: VulnDiff;
}

// diffKeys centralises the react-query cache keys for the compare view.
export const diffKeys = {
  all: ["imageDiff"] as const,
  compare: (org: string, repo: string, from: string, to: string) =>
    [...diffKeys.all, "compare", org, repo, from, to] as const,
};

// useImageDiff fetches the diff between two tags. Disabled until both tags are
// present. Diffs are effectively immutable for a given (from,to) digest pair,
// so a generous stale time avoids refetching while the operator reads.
export function useImageDiff(
  org: string,
  repo: string,
  from: string,
  to: string,
) {
  return useQuery({
    queryKey: diffKeys.compare(org, repo, from, to),
    queryFn: async (): Promise<ImageDiff> => {
      const { data } = await apiClient.get<ImageDiff>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/compare`,
        { params: { from, to } },
      );
      return data;
    },
    staleTime: 60_000,
    enabled: Boolean(org && repo && from && to),
  });
}

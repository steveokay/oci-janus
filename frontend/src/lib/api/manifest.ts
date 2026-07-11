import { useQuery } from "@tanstack/react-query";
import { AxiosError } from "axios";
import { apiClient } from "./client";

// Beacon — tag manifest detail (FE-API-002).
//
// The BFF parses the OCI raw_json server-side and hands back a structured
// view. We surface both single-arch (config + layers) and multi-arch
// (manifests[] with platform info) shapes; `is_index` is the explicit
// branch the UI uses.

export interface ManifestLayer {
  digest: string;
  size: number;
  media_type: string;
}

export interface ManifestConfig {
  digest: string;
  size: number;
  media_type: string;
}

export interface ManifestEntry {
  digest: string;
  size: number;
  media_type: string;
  architecture: string;
  os: string;
  variant?: string;
  os_version?: string;
}

// ImageProvenance mirrors the Go provenanceInfo block — the well-known OCI
// `org.opencontainers.image.*` annotations lifted off the manifest by the BFF.
// Every field is optional (omitempty on the wire). The URL-bearing fields
// (url, documentation, source) have already been sanitised server-side through
// safeExternalURL, so they are guaranteed http(s) or absent. `annotations` is
// a bounded raw view of ALL annotations (well-known + bespoke) for the
// collapsible raw table. Tier 2 #4 — Provenance tab.
export interface ImageProvenance {
  created?: string;
  authors?: string;
  url?: string;
  documentation?: string;
  source?: string;
  version?: string;
  revision?: string;
  vendor?: string;
  licenses?: string;
  ref_name?: string;
  title?: string;
  description?: string;
  base_name?: string;
  base_digest?: string;
  annotations?: Record<string, string>;
}

export interface ManifestDetail {
  digest: string;
  media_type: string;
  size_bytes: number;
  created_at: string;
  is_index: boolean;
  config: ManifestConfig;
  layers: ManifestLayer[];
  manifests: ManifestEntry[];
  // Tier 2 #4 — image lineage / provenance. Omitted on the wire (and
  // undefined here) when the manifest carries no OCI annotations at all (the
  // common case); the Provenance tab then renders its empty state.
  provenance?: ImageProvenance;
  // FE-API-050 — quarantine state. Omitted on the wire (and undefined
  // here) when the manifest is not quarantined (the common case). The
  // tag-detail Security tab renders a banner + lift dialog when set.
  quarantined?: boolean;
  quarantine_reason?: string;
  quarantined_at?: string;
  quarantined_by?: string;
}

export const manifestKeys = {
  all: ["manifest"] as const,
  byTag: (org: string, repo: string, tag: string) =>
    [...manifestKeys.all, "byTag", org, repo, tag] as const,
};

export function useManifest(org: string, repo: string, tag: string) {
  return useQuery({
    queryKey: manifestKeys.byTag(org, repo, tag),
    queryFn: async (): Promise<ManifestDetail | null> => {
      try {
        const { data } = await apiClient.get<ManifestDetail>(
          `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/tags/${encodeURIComponent(tag)}/manifest`,
        );
        return data;
      } catch (e) {
        // 404 → no manifest stored for this tag yet (shouldn't happen for a
        // tag we just listed, but keep the contract forgiving so the panel
        // can render an empty state instead of an error).
        if (e instanceof AxiosError && e.response?.status === 404) {
          return null;
        }
        throw e;
      }
    },
    staleTime: 60_000,
    enabled: Boolean(org && repo && tag),
  });
}

import { useQuery } from "@tanstack/react-query";
import type { ComponentProps } from "react";
import { apiClient } from "./client";
import type { Badge } from "@/components/ui/badge";

// Beacon — tag referrers (OCI referrers API surface).
//
// The BFF proxies registry-core's referrers listing for a tag and hands back
// the descriptors verbatim (digest / media_type / artifact_type / size, plus
// optional annotations). These are the signatures, SBOMs, and attestations
// attached to an image — previously only visible as raw JSON. `referrers` is
// ALWAYS an array on the wire (`[]` when empty); `annotations` is omitted
// entirely for a referrer that has none, so it's optional here.

export interface Referrer {
  digest: string;
  media_type: string;
  // artifact_type may be an empty string when the referrer didn't set one —
  // referrerTypeLabel falls back to media_type in that case.
  artifact_type: string;
  size: number;
  // Present only when the referrer carries annotations; treat as optional.
  annotations?: Record<string, string>;
}

export interface ReferrersResponse {
  referrers: Referrer[];
  // `filtered` reflects whether the registry applied an artifactType filter to
  // the listing. Always false today, but surfaced so the panel can note it
  // later without a wire change.
  filtered: boolean;
}

export const referrerKeys = {
  all: ["referrers"] as const,
  byTag: (org: string, repo: string, tag: string) =>
    [...referrerKeys.all, "byTag", org, repo, tag] as const,
};

// useReferrers loads the referrers attached to a tag's manifest. The endpoint
// wraps the list in `{ referrers: [...], filtered: bool }`; we defensively
// default `referrers` to [] so a nullish/empty body can't crash the table.
// A 404 (CORE_GRPC_ADDR unwired on this control plane) is intentionally NOT
// caught here — the panel special-cases it to render an info card rather than
// an error, mirroring admin/gc-card.tsx.
export function useReferrers(org: string, repo: string, tag: string) {
  return useQuery({
    queryKey: referrerKeys.byTag(org, repo, tag),
    queryFn: async (): Promise<ReferrersResponse> => {
      const { data } = await apiClient.get<ReferrersResponse>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/tags/${encodeURIComponent(tag)}/referrers`,
      );
      return {
        referrers: data.referrers ?? [],
        filtered: Boolean(data.filtered),
      };
    },
    staleTime: 20_000,
    enabled: Boolean(org && repo && tag),
  });
}

// Badge tone used for a referrer's friendly type chip. Kept alongside the
// label helper (and out of the component file) so the panel module only
// exports components — avoids a react-refresh/only-export-components lint hit.
type ReferrerTone = NonNullable<ComponentProps<typeof Badge>["tone"]>;

// referrerTypeLabel derives a human-friendly label from a referrer's
// artifact_type (preferred) or media_type (fallback). The classification is a
// substring match against the common OCI artifact type strings so it stays
// resilient to the exact vendor spelling (cosign vs. sigstore, spdx vs.
// cyclonedx, in-toto/dsse attestations, scan/vuln results).
export function referrerTypeLabel(r: Referrer): string {
  const at = r.artifact_type?.toLowerCase() ?? "";
  const mt = r.media_type?.toLowerCase() ?? "";
  // Prefer artifact_type; fall back to media_type when it's empty.
  const hint = at || mt;

  if (hint.includes("cosign") || hint.includes(".sig")) {
    return "Cosign signature";
  }
  if (hint.includes("spdx")) return "SBOM (SPDX)";
  if (hint.includes("cyclonedx")) return "SBOM (CycloneDX)";
  if (hint.includes("sbom")) return "SBOM";
  if (
    hint.includes("in-toto") ||
    hint.includes("attestation") ||
    hint.includes("dsse")
  ) {
    return "Attestation";
  }
  if (hint.includes("scan") || hint.includes("vuln")) return "Scan result";
  // A plain OCI image manifest with no artifact_type is a manifest referrer.
  if (!at && mt.includes("vnd.oci.image.manifest")) return "Image manifest";
  // Otherwise show the raw artifact_type if we have one, else a generic label.
  return r.artifact_type || "Artifact";
}

// referrerTypeTone maps the friendly label to a Badge tone. Signatures get the
// accent (the security-relevant highlight), scan results warn, everything else
// stays neutral so the table doesn't turn into a rainbow.
export function referrerTypeTone(r: Referrer): ReferrerTone {
  const label = referrerTypeLabel(r);
  if (label === "Cosign signature") return "accent";
  if (label === "Scan result") return "warning";
  return "neutral";
}

import { useMutation } from "@tanstack/react-query";
import { AxiosError } from "axios";
import { apiClient } from "./client";

// FE-API-033 — Per-tag SBOM download.
//
// Backend:
//   GET /api/v1/repositories/{org}/{repo}/tags/{tag}/sbom?format=spdx-json
//
// Default format is `spdx-json`. `cyclonedx-json` is reserved on the wire
// but the scanner doesn't emit it yet — the route returns 400 in that case
// so the UI should disable the option (we keep it visible as a future hint).
// When no SBOM has been generated, the route returns 404 with body
// `{ code: "no-sbom", error: "..." }`.

export type SbomFormat = "spdx-json" | "cyclonedx-json";

export const SBOM_FORMATS: Array<{ key: SbomFormat; label: string; available: boolean }> = [
  { key: "spdx-json", label: "SPDX 2.3 JSON", available: true },
  { key: "cyclonedx-json", label: "CycloneDX JSON (coming soon)", available: false },
];

interface DownloadArgs {
  org: string;
  repo: string;
  tag: string;
  format: SbomFormat;
}

interface DownloadError {
  status: "no-sbom" | "format-unsupported" | "network" | "unknown";
  message: string;
}

// useDownloadSbom — fetches the SBOM as a blob then triggers a browser
// download. Returning a mutation (vs. a query) means the click handler can
// surface error states with `toast` without re-renders, and we don't store
// the SBOM bytes in the query cache.
export function useDownloadSbom() {
  return useMutation<void, DownloadError, DownloadArgs>({
    mutationFn: async ({ org, repo, tag, format }) => {
      try {
        const res = await apiClient.get<Blob>(
          `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/tags/${encodeURIComponent(tag)}/sbom`,
          { params: { format }, responseType: "blob" },
        );
        // Browser save flow: build an object URL from the response blob and
        // create a transient <a download> click. The blob URL is released
        // after a tick so memory doesn't leak across repeated downloads.
        const blob = res.data;
        const filenameExt = format === "spdx-json" ? ".spdx.json" : ".cdx.json";
        const filename = `${repo}-${tag}${filenameExt}`;
        const url = window.URL.createObjectURL(blob);
        const a = document.createElement("a");
        a.href = url;
        a.download = filename;
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        window.setTimeout(() => window.URL.revokeObjectURL(url), 1_000);
      } catch (e) {
        // 404 → no-sbom is the most informative branch; 400 → format
        // unsupported. We don't have to parse the JSON body for `code` here
        // because each backend status maps 1:1 to our error.status.
        if (e instanceof AxiosError) {
          const status = e.response?.status;
          if (status === 404) {
            throw {
              status: "no-sbom",
              message:
                "No SBOM recorded for this tag yet — run a scan and try again.",
            } satisfies DownloadError;
          }
          if (status === 400) {
            throw {
              status: "format-unsupported",
              message:
                "Backend hasn't enabled this format yet. Pick SPDX JSON instead.",
            } satisfies DownloadError;
          }
        }
        throw {
          status: "network",
          message: "Couldn't fetch the SBOM. Check the BFF logs.",
        } satisfies DownloadError;
      }
    },
  });
}

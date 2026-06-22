import { useMutation, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "./client";
import { tagKeys } from "./tags";
import { manifestKeys } from "./manifest";

// Beacon — Manifest quarantine (FE-API-050).
//
// Backend routes:
//   POST /api/v1/repositories/{org}/{repo}/tags/{tag}/quarantine/lift
//
// The scanner sets quarantine automatically when a scan exceeds the
// effective block_on_severity policy (see FE-API-049 + worker.go
// hasPolicyViolation). There is intentionally NO "set quarantine"
// route from the FE today — manual quarantines would invite a
// denial-of-service shape via the UI. The lift route lets a repo
// admin / owner dismiss the gate after operator review.

interface LiftArgs {
  org: string;
  repo: string;
  tag: string;
}

interface LiftResponse {
  manifest_digest: string;
  quarantined: boolean;
}

// useLiftQuarantine — clears quarantine on the manifest pointed at by
// the tag. Invalidates the tags list (so the 🔒 pill disappears) and
// the per-tag manifest detail (so the Security tab banner clears).
//
// 403 surfaces when the caller is below repo admin/owner — gate
// matches the PUT scan-policy posture.
export function useLiftQuarantine() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ org, repo, tag }: LiftArgs) => {
      const { data } = await apiClient.post<LiftResponse>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/tags/${encodeURIComponent(tag)}/quarantine/lift`,
        {},
      );
      return data;
    },
    onSuccess: (_, { org, repo, tag }) => {
      // Tags list shows the pill, manifest detail shows the banner —
      // bust both so the operator sees the lift land everywhere.
      void qc.invalidateQueries({ queryKey: tagKeys.list(org, repo) });
      void qc.invalidateQueries({
        queryKey: manifestKeys.byTag(org, repo, tag),
      });
    },
  });
}

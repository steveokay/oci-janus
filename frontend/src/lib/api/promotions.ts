import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { apiClient } from "./client";

// FUT-020 — image promotion hooks.
//
// A promotion is a metadata-level atomic tag copy: the source tag's manifest
// digest is written onto a destination {org}/{repo}:{tag}. No blobs are
// copied — both tags reference the same manifest.
//
// The BFF routes are:
//   POST /api/v1/repositories/{org}/{repo}/tags/{tag}/promote
//   GET  /api/v1/repositories/{org}/{repo}/promotions
//
// The apiClient base URL already targets /api/v1 via the Vite proxy, so
// the paths below are relative — no proxy configuration change is required.

// Promotion mirrors the BFF's promotionResponse JSON shape. actor_user_id
// and note are optional in the wire (`omitempty` on the Go side); the type
// keeps them string-typed with an empty default so table cells never
// render "undefined".
export interface Promotion {
  id: string;
  src_org: string;
  src_repo: string;
  src_tag: string;
  src_digest: string;
  dst_org: string;
  dst_repo: string;
  dst_tag: string;
  dst_digest: string;
  actor_user_id?: string;
  note?: string;
  promoted_at: string;
  // FUT-020 follow-up — re-sign on promote. re_signed reports whether the
  // destination manifest carries a signature by the workspace key after the
  // promotion (true when re_sign_on_promote was requested and the signer
  // confirmed a signature, false otherwise). sign_error carries a reason when
  // a requested re-sign did not complete — the promotion is still durable.
  // Both are only present on the POST /promote response, never on history rows.
  re_signed?: boolean;
  sign_error?: string;
}

// PromoteInput is the shape the promote mutation accepts. The source is
// captured via the URL (org/repo/tag), so it does not repeat here.
export interface PromoteInput {
  dst_org: string;
  dst_repo: string;
  dst_tag: string;
  note?: string;
  // REM-030 — when true, the BFF asks the metadata surface to create
  // the destination repository if it doesn't exist. Default false
  // preserves the original 404-on-missing-dst behaviour so callers who
  // don't opt in don't accidentally create empty repos on typos.
  create_if_missing?: boolean;
  // FUT-020 follow-up — when true, after the promotion commits the BFF asks
  // registry-signer to sign the destination manifest with the workspace key.
  // Opting in when no signer is configured is rejected 400 before promoting.
  // Default false preserves the original promote-only behaviour.
  re_sign_on_promote?: boolean;
}

// promotionKeys centralises the react-query cache keys so hooks can
// invalidate coherently after a successful promote.
export const promotionKeys = {
  all: ["promotions"] as const,
  history: (org: string, repo: string) =>
    [...promotionKeys.all, "history", org, repo] as const,
};

// usePromoteTag is the mutation hook powering the PromoteTagDialog. On
// success we invalidate the history list for BOTH the source and the
// destination repos — the promotion touches both sides, so the "recent
// promotions" tab on either detail page must refresh.
export function usePromoteTag(org: string, repo: string, tag: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (input: PromoteInput): Promise<Promotion> => {
      const { data } = await apiClient.post<Promotion>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/tags/${encodeURIComponent(tag)}/promote`,
        input,
      );
      return data;
    },
    onSuccess: (data) => {
      // Invalidate history on the source repo (this repo) and the
      // destination repo so any Promotions tab open on either side refreshes.
      void qc.invalidateQueries({ queryKey: promotionKeys.history(org, repo) });
      void qc.invalidateQueries({
        queryKey: promotionKeys.history(data.dst_org, data.dst_repo),
      });
    },
  });
}

// PromotionListResponse is the envelope shape used by the GET route so
// json.RawMessage decoding on the FE has a stable field name.
interface PromotionListResponse {
  promotions: Promotion[];
}

// usePromotionHistory returns recent promotions touching this repo (src OR
// dst). The BFF caps limit at 50 today — no client-side pagination in v1;
// the "show more" affordance can land on top of this hook later without
// changing the shape.
export function usePromotionHistory(org: string, repo: string) {
  return useQuery({
    queryKey: promotionKeys.history(org, repo),
    queryFn: async (): Promise<Promotion[]> => {
      const { data } = await apiClient.get<PromotionListResponse>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/promotions`,
      );
      return data.promotions;
    },
    // Promotions are rare enough that a modest stale time keeps the UI
    // snappy without hammering the BFF. 30s matches the tags/manifest
    // hook baseline so the two panels update in a similar cadence.
    staleTime: 30_000,
    enabled: Boolean(org && repo),
  });
}

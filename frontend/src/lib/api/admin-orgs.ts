import { useMutation } from "@tanstack/react-query";
import { apiClient } from "./client";

// Platform-admin "claim a new org" hook.
//
// Closes the chicken-and-egg where a platform admin can't bootstrap a new
// org from the FE because the per-org admin check on /repositories rejects
// the (admin, org, "*") marker. The route grants the caller admin on the
// specified org so the existing repository-create flow then works.
//
// Idempotent — services/auth uses ON CONFLICT DO NOTHING on the
// role_assignments unique key, so re-claiming the same org is a 201 no-op.

export interface ClaimOrgResponse {
  org: string;
  granted_role: string;
}

export function useClaimOrg() {
  return useMutation({
    mutationFn: async (org: string): Promise<ClaimOrgResponse> => {
      const { data } = await apiClient.post<ClaimOrgResponse>(
        `/admin/orgs/${encodeURIComponent(org)}/claim`,
      );
      return data;
    },
  });
}

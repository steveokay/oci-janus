import { useQuery } from "@tanstack/react-query";
import { AxiosError } from "axios";
import { apiClient } from "./client";

// Beacon — tag signing status (FE-API-003).
//
// The BFF wraps signer.ListSignatures so the route works for both signed
// and unsigned tags. When SIGNER_GRPC_ADDR is unset on the management
// service, the route returns 404 "route disabled" — we map that to a
// disabled state so the UI explains *why* rather than showing an error.

export interface SignatureRecord {
  signer_id: string;
  key_id: string;
  signature_digest: string;
  signed_at: string;
}

export interface SignatureStatus {
  manifest_digest: string;
  signed: boolean;
  signatures: SignatureRecord[];
}

// Disabled is returned when the BFF responds with 404 "route disabled" —
// the signer client isn't wired on the management service. We expose this
// as a separate state so the UI can render a contextual message instead of
// claiming the tag is "unsigned".
export const SIGNING_DISABLED = Symbol("signing-disabled");
export type SignatureResult = SignatureStatus | typeof SIGNING_DISABLED;

export const signatureKeys = {
  all: ["signature"] as const,
  byTag: (org: string, repo: string, tag: string) =>
    [...signatureKeys.all, "byTag", org, repo, tag] as const,
};

export function useSignature(org: string, repo: string, tag: string) {
  return useQuery({
    queryKey: signatureKeys.byTag(org, repo, tag),
    queryFn: async (): Promise<SignatureResult> => {
      try {
        const { data } = await apiClient.get<SignatureStatus>(
          `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/tags/${encodeURIComponent(tag)}/signature`,
        );
        return data;
      } catch (e) {
        if (e instanceof AxiosError && e.response?.status === 404) {
          return SIGNING_DISABLED;
        }
        throw e;
      }
    },
    staleTime: 60_000,
    enabled: Boolean(org && repo && tag),
  });
}

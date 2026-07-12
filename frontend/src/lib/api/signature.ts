import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
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
  // FE-API-025: populated only when the request opts into ?verify=true.
  // Pointer-equivalent in JSON: omitted → caller didn't ask for verify;
  // present → carries the cryptographic verify outcome from the signer.
  verified?: boolean;
  failure_reason?: string;
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

// SignatureQueryOpts — verify gates the FE-API-025 opt-in. We key the query
// on verify so opting into verification creates a separate cache entry; the
// non-verified default path stays cheap and shared across tabs.
interface SignatureQueryOpts {
  verify?: boolean;
}

export function useSignature(
  org: string,
  repo: string,
  tag: string,
  opts: SignatureQueryOpts = {},
) {
  const verify = opts.verify === true;
  return useQuery({
    queryKey: [...signatureKeys.byTag(org, repo, tag), { verify }],
    queryFn: async (): Promise<SignatureResult> => {
      try {
        const { data } = await apiClient.get<SignatureStatus>(
          `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/tags/${encodeURIComponent(tag)}/signature`,
          verify ? { params: { verify: "true" } } : undefined,
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

// FE-API-026 — POST /sign hook. Server enforces repo-admin; we surface
// 409 / 400 separately so the UI can render "already signed by this key"
// vs a generic failure.
//
// FUT-009 — the signing identity is now named one of two mutually-exclusive
// ways. Exactly one of signer_id / service_account_id must be set:
//   - signer_id          → the legacy free-form string (cosign CLI parity).
//   - service_account_id → an SA chosen from the dashboard Select. Carries
//     the SA's shadow_user_id; the BFF validates it and records it as the
//     signature's signer_id.
// The body only carries whichever field the caller supplied — sending an
// empty string for the other would trip the BFF's "not both" guard.
interface SignManifestArgs {
  org: string;
  repo: string;
  tag: string;
  signer_id?: string;
  service_account_id?: string;
}

export function useSignManifest() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({
      org,
      repo,
      tag,
      signer_id,
      service_account_id,
    }: SignManifestArgs) => {
      // Build the body with only the field that was provided so the BFF sees
      // an unambiguous single identity.
      const payload = service_account_id
        ? { service_account_id }
        : { signer_id };
      const { data } = await apiClient.post<SignatureRecord>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/tags/${encodeURIComponent(tag)}/sign`,
        payload,
      );
      return data;
    },
    onSuccess: (_data, vars) => {
      // Invalidate both verify and non-verify entries so the panel reflects
      // the new signature on the next render.
      void qc.invalidateQueries({
        queryKey: signatureKeys.byTag(vars.org, vars.repo, vars.tag),
      });
    },
  });
}

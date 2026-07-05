import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { apiClient } from "./client";

// Beacon — TOTP MFA hooks.
//
// Backend surfaces (registry-auth):
//   GET    /api/v1/users/me/mfa                          → MfaStatus
//   POST   /api/v1/users/me/mfa/enroll                    → MfaEnrollResponse
//   POST   /api/v1/users/me/mfa/verify        body: { code }              → BackupCodesResponse
//   DELETE /api/v1/users/me/mfa               body: { password?, code? }
//   POST   /api/v1/users/me/mfa/backup-codes/regenerate
//                                              body: { password?, code? } → BackupCodesResponse
//
// All paths are folded under the shared /api/v1 apiClient baseURL.

// MfaStatus drives the settings card: whether MFA is enabled and, if so,
// when the user enrolled.
export interface MfaStatus {
  enabled: boolean;
  enrolled_at: string | null;
}

// MfaEnrollResponse carries the TOTP secret + otpauth URI used to render
// the QR code (via qrcode.react) during enrolment.
export interface MfaEnrollResponse {
  secret_base32: string;
  otpauth_uri: string;
}

// BackupCodesResponse is returned once — on first verification and again
// whenever the user regenerates codes. The backend never re-serves these.
export interface BackupCodesResponse {
  backup_codes: string[];
}

// ReauthBody accompanies MFA-disable and backup-code regeneration — the
// caller proves they still control the account via password or a live code.
interface ReauthBody {
  password?: string;
  code?: string;
}

export const mfaKeys = {
  status: ["mfa", "status"] as const,
};

// Current user's MFA status.
export function useMfaStatus() {
  return useQuery({
    queryKey: mfaKeys.status,
    queryFn: async () => {
      const { data } = await apiClient.get<MfaStatus>("/users/me/mfa");
      return data;
    },
  });
}

// Begin enrolment — returns the secret + otpauth URI for the QR code.
// Enrolment is not active until the first code is confirmed via
// useMfaVerify below.
export function useMfaEnroll() {
  return useMutation({
    mutationFn: async () => {
      const { data } = await apiClient.post<MfaEnrollResponse>(
        "/users/me/mfa/enroll",
      );
      return data;
    },
  });
}

// Confirm enrolment with the first TOTP code. Success activates MFA and
// returns the one-time backup codes.
export function useMfaVerify() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (code: string) => {
      const { data } = await apiClient.post<BackupCodesResponse>(
        "/users/me/mfa/verify",
        { code },
      );
      return data;
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: mfaKeys.status });
    },
  });
}

// Disable MFA. Requires re-auth (password or a current code) since this
// removes a security factor from the account.
export function useMfaDisable() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (reauth: ReauthBody) => {
      await apiClient.delete("/users/me/mfa", { data: reauth });
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: mfaKeys.status });
    },
  });
}

// Regenerate backup codes, invalidating any previously issued set.
// Requires re-auth for the same reason as disable.
export function useRegenerateBackupCodes() {
  return useMutation({
    mutationFn: async (reauth: ReauthBody) => {
      const { data } = await apiClient.post<BackupCodesResponse>(
        "/users/me/mfa/backup-codes/regenerate",
        reauth,
      );
      return data;
    },
  });
}

import { apiClientRaw } from "./client";
import { authStore } from "@/lib/auth/store";

// Auth-layer API. Login and logout both bypass the interceptor stack on
// purpose — login has no token yet, and logout must not retry through the
// refresh path when the server hangs up the JTI.

export interface LoginResponse {
  // The auth service returns BOTH `token` and `session_token` depending on
  // the call path. The dashboard login uses the JWT field; treat session as
  // a fallback to keep the shape forgiving.
  token?: string;
  session_token?: string;
  expires_in?: number;
  expires_at?: string;
  user_id?: string;
  token_type?: string;
  // MFA branches (Task 13). The /login handler now returns ONE of three
  // shapes: a full token (success), an MFA challenge (user already enrolled),
  // or an MFA setup requirement (forced enrolment — no factor yet).
  mfa_required?: boolean;
  challenge_token?: string;
  mfa_setup_required?: boolean;
  setup_token?: string;
}

// LoginResult is the discriminated outcome of a password login. The caller
// (login.tsx) branches on `kind` to either navigate (token) or render the
// second-factor / forced-enrolment step.
export type LoginResult =
  | { kind: "token" }
  | { kind: "mfa"; challengeToken: string }
  | { kind: "mfa_setup"; setupToken: string };

export async function login(
  username: string,
  password: string,
  tenantId: string,
): Promise<LoginResult> {
  // The auth service's /api/v1/login handler expects a tenant_id alongside
  // the credentials. In production the gateway resolves tenant from the
  // request hostname; in dev we pass it explicitly from VITE_DEFAULT_TENANT_ID.
  const { data } = await apiClientRaw.post<LoginResponse>("/login", {
    tenant_id: tenantId,
    username,
    password,
  });

  // MFA challenge — the user is enrolled and must complete the OTP step. No
  // token is stored yet; the caller hands the challenge_token to loginMfa.
  if (data.mfa_required && data.challenge_token) {
    return { kind: "mfa", challengeToken: data.challenge_token };
  }
  // Forced enrolment — the tenant/policy requires MFA but the user has no
  // factor yet. The setup_token authorises the enroll+verify calls that
  // complete enrolment and mint a real access token.
  if (data.mfa_setup_required && data.setup_token) {
    return { kind: "mfa_setup", setupToken: data.setup_token };
  }

  // Plain success — preserve the existing `token` / `session_token` fallback
  // so we never silently change which key we store.
  const token = data.token ?? data.session_token;
  if (!token) {
    throw new Error("Login succeeded but no token was returned.");
  }
  authStore.setToken(token);
  return { kind: "token" };
}

// loginMfa completes the second-factor step. `code` is either a 6-digit TOTP
// or a backup code — the backend accepts either on the same endpoint. On
// success it returns a full access token, which we store. A wrong code yields
// a 401 the caller surfaces as a retry prompt.
export async function loginMfa(
  challengeToken: string,
  code: string,
): Promise<void> {
  // Bypass the interceptor stack (apiClientRaw): there's no session yet, and
  // /login/mfa is in NO_REFRESH_PATHS so a 401 must not trigger a refresh loop.
  const { data } = await apiClientRaw.post<{ token: string }>("/login/mfa", {
    challenge_token: challengeToken,
    code,
  });
  authStore.setToken(data.token);
}

// enrollWithSetupToken begins forced enrolment during login. Unlike the
// self-service enroll hook (which relies on the session bearer), there is no
// session yet — the setup token IS the authorisation, sent as an explicit
// bearer. apiClientRaw carries no request interceptor, so it does not clobber
// this per-request Authorization header.
export async function enrollWithSetupToken(
  setupToken: string,
): Promise<{ secret_base32: string; otpauth_uri: string }> {
  const { data } = await apiClientRaw.post<{
    secret_base32: string;
    otpauth_uri: string;
  }>("/users/me/mfa/enroll", undefined, {
    headers: { Authorization: `Bearer ${setupToken}` },
  });
  return data;
}

// verifyWithSetupToken confirms forced enrolment with the first TOTP code.
// On the setup-token path the response ADDITIONALLY carries a full access
// token alongside the backup codes, so the user ends the flow logged in.
export async function verifyWithSetupToken(
  setupToken: string,
  code: string,
): Promise<{ backup_codes: string[]; token?: string }> {
  const { data } = await apiClientRaw.post<{
    backup_codes: string[];
    token?: string;
  }>(
    "/users/me/mfa/verify",
    { code },
    { headers: { Authorization: `Bearer ${setupToken}` } },
  );
  return data;
}

export async function logout(): Promise<void> {
  const token = authStore.getToken();
  // Best-effort server revoke. Clearing local state is what matters for the
  // user's "I am logged out" expectation — we run that even if the call fails.
  try {
    if (token) {
      await apiClientRaw.post(
        "/logout",
        {},
        { headers: { Authorization: `Bearer ${token}` } },
      );
    }
  } finally {
    authStore.clear();
  }
}

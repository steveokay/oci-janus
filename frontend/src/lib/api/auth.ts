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
}

export async function login(
  username: string,
  password: string,
  tenantId: string,
): Promise<string> {
  // The auth service's /api/v1/login handler expects a tenant_id alongside
  // the credentials. In production the gateway resolves tenant from the
  // request hostname; in dev we pass it explicitly from VITE_DEFAULT_TENANT_ID.
  const { data } = await apiClientRaw.post<LoginResponse>("/login", {
    tenant_id: tenantId,
    username,
    password,
  });
  const token = data.token ?? data.session_token;
  if (!token) {
    throw new Error("Login succeeded but no token was returned.");
  }
  authStore.setToken(token);
  return token;
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

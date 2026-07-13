import { useQuery } from "@tanstack/react-query";
import { apiClient } from "./client";

// FUT-084 — SSO login (frontend).
//
// The backend OAuth-PKCE + SAML dance is fully built in registry-auth. The FE
// only has to (1) fetch the CONFIGURED providers, (2) start the redirect dance
// on click, and (3) consume the `sso_token` the auth service hands back on the
// callback redirect. This module owns (1) and the redirect plumbing for (2)/(3);
// the login route consumes the token (see routes/login.tsx beforeLoad).

// SSOProvider mirrors registry-auth's providerListItem
// (services/auth/internal/handler/sso.go). `id` is a stable string
// ("google", "github", "microsoft", or a SAML provider id); `type` is the
// kind ("oauth_google", "oauth_github", "oauth_microsoft", "oauth_oidc",
// "saml"); `login_url` is the intra-app /start path to navigate to.
export interface SSOProvider {
  id: string;
  type: string;
  display_name: string;
  login_url: string;
}

interface ProvidersResponse {
  providers: SSOProvider[];
}

async function fetchProviders(): Promise<SSOProvider[]> {
  // Public, unauthenticated endpoint — auth owns /api/v1/auth/* (the apiClient
  // base is /api/v1). Enabled-only; empty array when SSO isn't configured.
  const { data } = await apiClient.get<ProvidersResponse>("/auth/providers");
  return data.providers ?? [];
}

// useAuthProviders returns the enabled global SSO providers. Cached for the
// session — the set only changes when an operator reconfigures + redeploys.
export function useAuthProviders() {
  return useQuery({
    queryKey: ["auth-providers"],
    queryFn: fetchProviders,
    staleTime: 60 * 60_000, // 1h — provider config is deploy-time, not runtime
    gcTime: 60 * 60_000,
    retry: 1, // public endpoint; one retry covers a transient blip
  });
}

// Where the post-SSO destination is stashed. sessionStorage survives the
// full-page redirect to the IdP and back (same origin, same tab); the
// in-memory auth store does not, which is exactly why we can't keep it there.
const RETURN_TO_KEY = "sso_return_to";

// safeInternalPath rejects anything that isn't an internal absolute path so a
// tampered return target can't become an open redirect: must start with a
// single "/" and not "//" (protocol-relative → external).
function safeInternalPath(p: string | null | undefined): string {
  if (p && p.startsWith("/") && !p.startsWith("//")) return p;
  return "/";
}

// prepareSSOLogin stashes where to land after the round-trip and returns the
// auth-service /start URL to navigate to. `next` is always /login: the auth
// service appends `?sso_token=<jwt>` to it, and login's beforeLoad consumes
// the token then bounces to the stashed return path. Kept pure (no navigation)
// so it's unit-testable; beginSSOLogin does the actual navigation.
export function prepareSSOLogin(loginURL: string, from: string | undefined): string {
  try {
    sessionStorage.setItem(RETURN_TO_KEY, safeInternalPath(from));
  } catch {
    // sessionStorage unavailable (private-mode edge) — non-fatal; we just lose
    // the return target and land on home after login.
  }
  return `${loginURL}?next=${encodeURIComponent("/login")}`;
}

// beginSSOLogin stashes the return target and navigates to the provider's
// /start URL, kicking off the full-page OAuth/SAML redirect dance.
export function beginSSOLogin(loginURL: string, from: string | undefined): void {
  window.location.assign(prepareSSOLogin(loginURL, from));
}

// consumeSSOReturnTo reads + clears the stashed post-login destination,
// re-sanitised to an internal path (defence in depth against a tampered
// sessionStorage value). Defaults to "/".
export function consumeSSOReturnTo(): string {
  let raw: string | null = null;
  try {
    raw = sessionStorage.getItem(RETURN_TO_KEY);
    sessionStorage.removeItem(RETURN_TO_KEY);
  } catch {
    // ignore — treat as no stash.
  }
  return safeInternalPath(raw);
}

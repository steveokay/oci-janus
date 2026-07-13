import * as React from "react";
import { Github, KeyRound } from "lucide-react";
import { cn } from "@/lib/utils";
import {
  useAuthProviders,
  beginSSOLogin,
  type SSOProvider,
} from "@/lib/api/sso";

// Beacon — SSO sign-in buttons (FUT-084).
//
// Renders one button per CONFIGURED provider returned by
// GET /api/v1/auth/providers (enabled-only, deploy-time config). Clicking a
// button starts the full-page OAuth/SAML redirect dance via beginSSOLogin;
// the auth service hands the JWT back as ?sso_token= on /login, which the
// login route's beforeLoad consumes (see routes/login.tsx).
//
// When no providers are configured — or while the list is loading / errored —
// this renders nothing, so the password form stands alone with no dead chrome.
//
// Brand glyphs: Google + Microsoft are inline SVG (lucide ships no brand
// marks); GitHub + a generic key (SAML / OIDC) come from lucide.

interface SSOButtonsProps {
  className?: string;
  // Where to land after a successful sign-in — the same post-login target the
  // password path honours (the ?from= the auth guard bounced the user from).
  // Stashed across the redirect round-trip; open-redirect-guarded downstream.
  from?: string;
}

export function SSOButtons({ className, from }: SSOButtonsProps): React.ReactElement | null {
  const { data: providers } = useAuthProviders();

  // Nothing to show until we have a non-empty configured list. `undefined`
  // covers both the loading and the errored states (retry:1 then error) — in
  // every "not ready" case we render nothing rather than flash placeholder UI.
  if (!providers || providers.length === 0) {
    return null;
  }

  return (
    <div className={className}>
      <div className="grid grid-cols-2 gap-2">
        {providers.map((p) => (
          <button
            key={p.id}
            type="button"
            onClick={() => beginSSOLogin(p.login_url, from)}
            className={cn(
              "group inline-flex items-center justify-center gap-2 rounded-md",
              "border border-[var(--color-border-strong)] bg-[var(--color-surface)]",
              "px-3 py-2.5 text-sm font-medium text-[var(--color-fg)]",
              "transition-colors hover:bg-[var(--color-surface-sunken)]",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]",
            )}
            aria-label={`Continue with ${p.display_name}`}
          >
            <span className="shrink-0">{iconForProvider(p)}</span>
            <span className="truncate">{p.display_name}</span>
          </button>
        ))}
      </div>

      {/* Divider lives here so it only appears when there's at least one
          provider — no orphaned "or" line on a password-only deployment. */}
      <div className="relative my-5">
        <div className="absolute inset-x-0 top-1/2 h-px bg-[var(--color-border)]" />
        <div className="relative flex justify-center">
          <span className="bg-[var(--color-surface)] px-3 text-[10px] font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
            or sign in with credentials
          </span>
        </div>
      </div>
    </div>
  );
}

// iconForProvider picks a brand glyph from the provider's kind/id. Falls back
// to a generic key for SAML and any generic-OIDC provider.
function iconForProvider(p: SSOProvider): React.ReactNode {
  const key = `${p.type} ${p.id}`.toLowerCase();
  if (key.includes("google")) return <GoogleIcon />;
  if (key.includes("github")) return <Github className="size-4" />;
  if (key.includes("microsoft")) return <MicrosoftIcon />;
  return <KeyRound className="size-4" />;
}

// ─── inline brand glyphs ───────────────────────────────────────────────────
// Sized at 16x16 to match lucide's default; viewBox set per source artwork.

function GoogleIcon(): React.ReactElement {
  return (
    <svg
      viewBox="0 0 24 24"
      width="16"
      height="16"
      aria-hidden
      xmlns="http://www.w3.org/2000/svg"
    >
      <path
        d="M21.35 11.1H12v3.2h5.35c-.24 1.4-1 2.6-2.13 3.4l3.43 2.65c2-1.85 3.15-4.55 3.15-7.7 0-.6-.05-1.1-.15-1.55z"
        fill="#4285F4"
      />
      <path
        d="M12 22c2.85 0 5.25-.95 7-2.55l-3.43-2.65c-.95.65-2.17 1.05-3.57 1.05-2.75 0-5.07-1.85-5.9-4.35H2.5v2.7C4.25 19.6 7.85 22 12 22z"
        fill="#34A853"
      />
      <path
        d="M6.1 13.5c-.2-.65-.32-1.35-.32-2.05s.12-1.4.32-2.05V6.7H2.5C1.8 8.1 1.4 9.7 1.4 11.45c0 1.75.4 3.35 1.1 4.75l3.6-2.7z"
        fill="#FBBC05"
      />
      <path
        d="M12 5.5c1.55 0 2.95.55 4.05 1.6l3.05-3.05C17.25 2.3 14.85 1.4 12 1.4 7.85 1.4 4.25 3.8 2.5 7.2l3.6 2.7c.83-2.5 3.15-4.4 5.9-4.4z"
        fill="#EA4335"
      />
    </svg>
  );
}

function MicrosoftIcon(): React.ReactElement {
  return (
    <svg
      viewBox="0 0 24 24"
      width="16"
      height="16"
      aria-hidden
      xmlns="http://www.w3.org/2000/svg"
    >
      <rect x="2" y="2" width="9" height="9" fill="#F25022" />
      <rect x="13" y="2" width="9" height="9" fill="#7FBA00" />
      <rect x="2" y="13" width="9" height="9" fill="#00A4EF" />
      <rect x="13" y="13" width="9" height="9" fill="#FFB900" />
    </svg>
  );
}

import * as React from "react";
import { Github, KeyRound } from "lucide-react";
import { cn } from "@/lib/utils";

// Beacon — SSO sign-in buttons.
//
// The backend OAuth/SAML flow is tracked as Sprint 1a in status.md (not yet
// implemented). The buttons render in their final shape so the moment a
// provider lands we only swap the click handler — no layout churn.
//
// Each provider has its own brand icon. We render Google + Microsoft + SAML
// inline as SVG (lucide-react doesn't ship brand glyphs) and pull GitHub
// from lucide because it's the one brand mark they do ship.

type Provider = {
  id: "google" | "github" | "microsoft" | "saml";
  label: string;
  icon: React.ReactNode;
};

const PROVIDERS: Provider[] = [
  {
    id: "google",
    label: "Continue with Google",
    icon: <GoogleIcon />,
  },
  {
    id: "github",
    label: "Continue with GitHub",
    icon: <Github className="size-4" />,
  },
  {
    id: "microsoft",
    label: "Continue with Microsoft",
    icon: <MicrosoftIcon />,
  },
  {
    id: "saml",
    label: "SAML / SSO",
    icon: <KeyRound className="size-4" />,
  },
];

interface SSOButtonsProps {
  className?: string;
}

export function SSOButtons({ className }: SSOButtonsProps): React.ReactElement {
  // UIR-5: the login-page SSO flow isn't wired yet (the old handler only
  // toasted "coming soon" while the buttons rendered fully active — they
  // looked clickable but did nothing). Render them visibly disabled with a
  // caption so the state is honest. When the client flow lands, drop
  // `disabled`, add the click handler, and delete this note:
  //   - Generate a state token, persist in sessionStorage
  //   - window.location = `/auth/sso/${provider.id}?state=…`
  //   - Callback route exchanges the code for a JWT via the auth service
  return (
    <div className={className}>
      <div className="grid grid-cols-2 gap-2">
        {PROVIDERS.map((p) => (
          <button
            key={p.id}
            type="button"
            disabled
            title="Single sign-on launches with the next release"
            className={cn(
              "group inline-flex items-center justify-center gap-2 rounded-md",
              "border border-[var(--color-border-strong)] bg-[var(--color-surface)]",
              "px-3 py-2.5 text-sm font-medium text-[var(--color-fg)]",
              "transition-colors hover:bg-[var(--color-surface-sunken)]",
              "focus-visible:outline-none",
              "disabled:cursor-not-allowed disabled:opacity-55 disabled:hover:bg-[var(--color-surface)]",
            )}
            aria-label={`${p.label} (coming soon)`}
          >
            <span className="shrink-0">{p.icon}</span>
            <span className="truncate">{shortLabel(p)}</span>
          </button>
        ))}
      </div>
      <p className="mt-2 text-center text-[11px] text-[var(--color-fg-subtle)]">
        Single sign-on is coming soon.
      </p>
    </div>
  );
}

// "Continue with Google" is too long for the 2-col grid on narrow viewports.
// We shorten in the button face but keep the full string as the aria-label.
function shortLabel(p: Provider): string {
  switch (p.id) {
    case "google":
      return "Google";
    case "github":
      return "GitHub";
    case "microsoft":
      return "Microsoft";
    case "saml":
      return "SAML";
  }
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

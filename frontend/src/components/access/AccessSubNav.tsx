import * as React from "react";
import { Link } from "@tanstack/react-router";
import {
  KeyRound,
  Bot,
  Activity,
  ShieldCheck,
  Terminal,
  FileKey,
  ClipboardCheck,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import { useAuthStore } from "@/lib/auth/store";
import { isWorkspaceAdmin } from "@/lib/auth/jwt";

// Shape for a single sub-nav entry. `preview` items are shown at lower
// contrast with a "Preview" pill — they link to real routes that will carry
// dummy data until T25-T28 land (FUT-001..FUT-004).
interface SubNavItem {
  to: string;
  label: string;
  icon: typeof KeyRound;
  preview?: boolean;
}

interface SubNavSection {
  title: string;
  items: SubNavItem[];
  // When `adminOnly` is true the entire section is hidden for non-admins.
  adminOnly?: boolean;
}

// Static nav structure for the /api-keys hub.
// The route paths for T25-T28 (service-accounts, activity, trust, helpers,
// policies, review) are wired here now; TanStack Router 404s on click until
// each corresponding leaf route is created. That is the accepted behaviour
// for T24's deliverable.
const SECTIONS: SubNavSection[] = [
  {
    title: "Yours",
    items: [
      {
        to: "/api-keys",
        label: "Personal keys",
        icon: KeyRound,
      },
    ],
  },
  {
    title: "Workspace",
    adminOnly: true,
    items: [
      {
        to: "/api-keys/service-accounts",
        label: "Service accounts",
        icon: Bot,
      },
      {
        to: "/api-keys/activity",
        label: "Activity",
        icon: Activity,
      },
    ],
  },
  {
    title: "Preview",
    adminOnly: true,
    items: [
      {
        to: "/api-keys/trust",
        label: "Federated trust",
        icon: ShieldCheck,
        preview: true,
      },
      {
        to: "/api-keys/helpers",
        label: "Credential helpers",
        icon: Terminal,
        preview: true,
      },
      {
        to: "/api-keys/policies",
        label: "Token policies",
        icon: FileKey,
        preview: true,
      },
      {
        to: "/api-keys/review",
        label: "Access review",
        icon: ClipboardCheck,
        preview: true,
      },
    ],
  },
];

// AccessSubNav — vertical rail rendered on the left side of the /api-keys
// hub by `AccessHubLayout`. Uses TanStack Router's `<Link>` for active-state
// highlighting; admin-gated sections are omitted entirely for non-admins.
export function AccessSubNav(): React.ReactElement {
  const claims = useAuthStore((s) => s.claims);
  const isAdmin = isWorkspaceAdmin(claims);

  return (
    <nav
      aria-label="Access"
      className="w-48 shrink-0 space-y-5"
    >
      {SECTIONS.map((section) => {
        // Hide the entire section for non-admins when it's admin-only.
        if (section.adminOnly && !isAdmin) return null;

        return (
          <div key={section.title}>
            {/* Section heading — caps label, same weight as the sidebar. */}
            <div className="mb-1 px-3 text-[10px] font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
              {section.title}
            </div>

            <ul className="space-y-0.5">
              {section.items.map((item) => {
                const Icon = item.icon;
                return (
                  <li key={item.to}>
                    <Link
                      to={item.to}
                      // `activeProps` highlights the exact-matched route.
                      // For the index route (/api-keys) we want exact matching
                      // so navigating to /api-keys/service-accounts doesn't
                      // keep "Personal keys" highlighted.
                      activeProps={{
                        className:
                          "bg-[var(--color-accent-subtle)] text-[var(--color-accent)]",
                      }}
                      activeOptions={{ exact: item.to === "/api-keys" }}
                      className={cn(
                        "group flex items-center gap-2 rounded-md px-3 py-2 text-sm",
                        "transition-colors",
                        // Default (inactive) appearance — matches sidebar item style.
                        item.preview
                          ? "text-[var(--color-fg-subtle)] hover:bg-[var(--color-surface-sunken)] hover:text-[var(--color-fg-muted)]"
                          : "text-[var(--color-fg)] hover:bg-[var(--color-surface-sunken)]",
                      )}
                    >
                      <Icon
                        className={cn(
                          "size-4 shrink-0",
                          item.preview
                            ? "text-[var(--color-fg-subtle)] group-hover:text-[var(--color-fg-muted)]"
                            : "text-[var(--color-fg-muted)] group-hover:text-[var(--color-fg)]",
                        )}
                      />

                      <span className="flex-1 truncate">{item.label}</span>

                      {/* Preview pill — small amber badge for FUT-001..FUT-004 routes. */}
                      {item.preview ? (
                        <Badge
                          tone="warning"
                          className="py-0 text-[9px] uppercase tracking-wider"
                        >
                          Preview
                        </Badge>
                      ) : null}
                    </Link>
                  </li>
                );
              })}
            </ul>
          </div>
        );
      })}
    </nav>
  );
}

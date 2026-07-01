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

// Shape for a single sub-nav entry. The `preview` flag was used by
// the FUT-001..FUT-004 batch to render a low-contrast "Preview" pill
// on links that pointed at in-development surfaces; with FUT-004's
// graduation the flag is no longer set on any entry but is retained
// on the type for future preview surfaces.
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

// Static nav structure for the /api-keys hub. All routes are live as
// of the FUT-001..FUT-004 graduation on 2026-07-01.
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
      {
        // FUT-002 shipped 2026-06-30 — graduated out of the Preview section.
        to: "/api-keys/helpers",
        label: "Credential helpers",
        icon: Terminal,
      },
      {
        // FUT-001 shipped 2026-07-01 — graduated out of the Preview section.
        to: "/api-keys/trust",
        label: "Federated trust",
        icon: ShieldCheck,
      },
      {
        // FUT-003 shipped 2026-07-01 — graduated out of the Preview section.
        to: "/api-keys/policies",
        label: "Token policies",
        icon: FileKey,
      },
      {
        // FUT-004 shipped 2026-07-01 — graduated out of the Preview
        // section. FUT-004 is the LAST FUT in the FUT-001..FUT-004
        // batch; with its graduation the entire Preview section
        // (flyout expander + localStorage plumbing) was removed.
        to: "/api-keys/review",
        label: "Access review",
        icon: ClipboardCheck,
      },
    ],
  },
];

// AccessSubNav — vertical rail rendered on the left side of the /api-keys
// hub by `AccessHubLayout`. Uses TanStack Router's `<Link>` for active-state
// highlighting; admin-gated sections are omitted entirely for non-admins.
//
// DSGN-011 — the Preview section (FUT-001..FUT-004) previously wrapped
// four in-development surfaces behind a collapsible expander. With
// FUT-004's graduation on 2026-07-01 the entire FUT batch shipped and
// the Preview section was removed from SECTIONS. The localStorage
// plumbing (readPreviewOpen / PREVIEW_OPEN_KEY) is retained at module
// scope as dead code — dropping it is a follow-up cleanup once we're
// sure no future preview surfaces will reinstate the section. The
// component-local state / toggle helper were removed to keep lint
// clean.
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
              {section.items.map((item) => (
                <SubNavLink key={item.to} item={item} />
              ))}
            </ul>
          </div>
        );
      })}
    </nav>
  );
}

// SubNavLink — single nav row. Extracted so the Preview flyout and the
// always-visible sections can share the same rendering without duplicating
// the active-state / preview-pill logic.
function SubNavLink({ item }: { item: SubNavItem }): React.ReactElement {
  const Icon = item.icon;
  return (
    <li>
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
}

import * as React from "react";
import { useNavigate } from "@tanstack/react-router";
import { LogOut, User as UserIcon, ChevronDown, Bot, Menu } from "lucide-react";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { toast } from "sonner";
import { ThemeToggle } from "./theme-toggle";
import { NotificationsBell } from "./notifications-bell";
import { Button } from "@/components/ui/button";
import { useAuthStore } from "@/lib/auth/store";
import { logout } from "@/lib/api/auth";
import { useMe } from "@/lib/api/me";
import { useDeploymentInfo, isSingleMode } from "@/lib/api/deployment-info";

// Beacon — Topbar. Slim 56px bar with breadcrumb area on the left and
// account + theme on the right. The breadcrumb slot is intentionally a
// passed-in child so route components can render whatever context fits.
//
// FE-API-048 T29: the avatar now branches on `/users/me` `type`.
// - type === "service_account" → BotAvatar (bot icon + SA name, no menu)
// - otherwise                  → existing human ProfileChip with dropdown

interface TopbarProps {
  breadcrumb?: React.ReactNode;
  // REDESIGN-001 Phase 4.6 — hamburger trigger callback. The drawer
  // state is owned by AppShell; Topbar just signals the open intent.
  // Optional so the bare Topbar can still mount in tests / Storybook
  // without forcing every caller to wire the drawer.
  onOpenMobileNav?: () => void;
}

// BotAvatar renders when the authenticated principal is a service account.
// A rounded-square chip (teal-100 bg, teal-600 icon) signals the distinct
// identity type; the SA name appears beside it on medium+ screens.
// No clickable menu is provided — SAs have no profile page or logout action.
interface BotAvatarProps {
  name: string;
}

function BotAvatar({ name }: BotAvatarProps): React.ReactElement {
  return (
    <span
      className="flex items-center gap-2 rounded-md px-2 py-1"
      aria-label={`Service account: ${name}`}
      title="Authenticated as service account"
    >
      {/* Rounded-square chip with bot icon — visually distinct from the
          circular human avatar (rounded-full, accent-subtle background). */}
      <span
        className="grid size-7 place-items-center rounded-md bg-[var(--color-teal-100)] text-[var(--color-teal-700)]"
        aria-hidden
      >
        <Bot className="size-4" />
      </span>
      <span className="hidden text-sm font-medium md:inline">{name}</span>
    </span>
  );
}

export function Topbar({
  breadcrumb,
  onOpenMobileNav,
}: TopbarProps): React.ReactElement {
  const claims = useAuthStore((s) => s.claims);
  const navigate = useNavigate();
  // Fetch the full /users/me response so we can branch on principal type.
  // The query is already mounted by the app shell, so this is a cache-hit in
  // the common case (no extra network round-trip).
  const { data: me } = useMe();

  // REDESIGN-001 Phase 2.5 (RM-007 + HD-001) — gate the tenant UUID chip
  // shown in the avatar dropdown on multi mode. In single-tenant there's
  // only one tenant and surfacing its UUID is meaningless chrome — and a
  // small disclosure surface to anyone standing behind the operator. While
  // the cache is cold we default to multi-mode behaviour (show the chip)
  // because the existing UX already showed it; suppressing during cold
  // load would cause a brief flash if the user signs in then immediately
  // opens the dropdown.
  const { data: deploymentInfo } = useDeploymentInfo();
  const singleMode = isSingleMode(deploymentInfo);

  // Defensive default: treat missing or unrecognised type as "user" so the
  // human avatar path is always shown on pre-T16 backends.
  const isServiceAccount = (me?.type ?? "user") === "service_account";
  // SA display name comes from the nested service_account object; fall back
  // to display_name on the outer response if the nested field is absent.
  const saName =
    me?.service_account?.name ?? me?.display_name ?? "Service Account";

  // The dev admin JWT doesn't carry a `username` claim, only `sub`. Showing
  // the literal string "User" looked sloppy; instead, derive an initial from
  // sub when username is missing, and hide the label text on the chip so the
  // avatar + tenant_id chip stand alone.
  const username = claims?.username;
  const initial =
    (username?.[0] ?? claims?.sub?.[0] ?? "·").toUpperCase();

  async function handleLogout(): Promise<void> {
    await logout();
    toast.success("Signed out.");
    void navigate({ to: "/login", replace: true });
  }

  // UIR-7: the topbar is a non-scrolling flex sibling of the
  // `overflow-y-auto` <main>, so `sticky top-0` / `backdrop-blur` / the `/85`
  // translucency never fired (nothing scrolls underneath it). Dropped the dead
  // styles for a solid, opaque bar rather than moving it inside the scroll
  // container (which would make it scroll away with the content).
  return (
    <header className="flex h-14 items-center justify-between gap-4 border-b border-[var(--color-border)] bg-[var(--color-bg)] px-6">
      <div className="flex min-w-0 items-center gap-3">
        {/* Phase 4.6 — hamburger that opens the mobile drawer. Hidden lg+
            where the desktop sidebar is visible. Sits before the
            breadcrumb so the visual order is: nav handle → location
            context, matching native mobile patterns. */}
        {onOpenMobileNav ? (
          <button
            type="button"
            onClick={onOpenMobileNav}
            aria-label="Open navigation"
            className="grid size-9 place-items-center rounded-md text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-sunken)] focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/40 lg:hidden"
          >
            <Menu className="size-4" />
          </button>
        ) : null}
        {breadcrumb}
      </div>
      <div className="flex items-center gap-1">
        <NotificationsBell />
        <ThemeToggle />

        {/* Service-account branch: bot avatar only, no dropdown menu.
            Human branch: existing ProfileChip with dropdown. */}
        {isServiceAccount ? (
          <BotAvatar name={saName} />
        ) : (
          <DropdownMenu.Root>
            <DropdownMenu.Trigger asChild>
              <Button variant="ghost" size="sm" className="gap-2 px-2">
                <span
                  className="grid size-7 place-items-center rounded-full bg-[var(--color-accent-subtle)] text-xs font-semibold text-[var(--color-accent)]"
                  aria-hidden
                >
                  {initial}
                </span>
                {username ? (
                  <span className="hidden text-sm md:inline">{username}</span>
                ) : null}
                <ChevronDown className="size-3.5 text-[var(--color-fg-muted)]" />
              </Button>
            </DropdownMenu.Trigger>
            <DropdownMenu.Portal>
              <DropdownMenu.Content
                align="end"
                sideOffset={6}
                className="z-50 min-w-52 overflow-hidden rounded-md border border-[var(--color-border)] bg-[var(--color-surface-2)] p-1 shadow-[var(--shadow-floating)]"
              >
                <div className="px-2 pb-2 pt-1.5">
                  <div className="text-sm font-medium">
                    {username ?? (
                      <span className="font-mono text-[var(--color-fg-muted)]">
                        {claims?.sub?.slice(0, 8) ?? "—"}…
                      </span>
                    )}
                  </div>
                  {singleMode ? null : (
                    <div className="truncate font-mono text-[11px] text-[var(--color-fg-muted)]">
                      {claims?.tenant_id ?? "—"}
                    </div>
                  )}
                </div>
                <DropdownMenu.Separator className="my-1 h-px bg-[var(--color-border)]" />
                <DropdownMenu.Item
                  onSelect={() => navigate({ to: "/profile" })}
                  className="flex cursor-pointer items-center gap-2 rounded-sm px-2 py-1.5 text-sm outline-none data-[highlighted]:bg-[var(--color-surface-sunken)]"
                >
                  <UserIcon className="size-4 text-[var(--color-fg-muted)]" />
                  Profile
                </DropdownMenu.Item>
                <DropdownMenu.Item
                  onSelect={() => void handleLogout()}
                  className="flex cursor-pointer items-center gap-2 rounded-sm px-2 py-1.5 text-sm outline-none data-[highlighted]:bg-[var(--color-surface-sunken)]"
                >
                  <LogOut className="size-4 text-[var(--color-fg-muted)]" />
                  Sign out
                </DropdownMenu.Item>
              </DropdownMenu.Content>
            </DropdownMenu.Portal>
          </DropdownMenu.Root>
        )}
      </div>
    </header>
  );
}

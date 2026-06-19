import * as React from "react";
import { useNavigate } from "@tanstack/react-router";
import { LogOut, User as UserIcon, ChevronDown } from "lucide-react";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { toast } from "sonner";
import { ThemeToggle } from "./theme-toggle";
import { Button } from "@/components/ui/button";
import { useAuthStore } from "@/lib/auth/store";
import { logout } from "@/lib/api/auth";

// Beacon — Topbar. Slim 56px bar with breadcrumb area on the left and
// account + theme on the right. The breadcrumb slot is intentionally a
// passed-in child so route components can render whatever context fits.

interface TopbarProps {
  breadcrumb?: React.ReactNode;
}

export function Topbar({ breadcrumb }: TopbarProps): React.ReactElement {
  const claims = useAuthStore((s) => s.claims);
  const navigate = useNavigate();

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

  return (
    <header className="sticky top-0 z-30 flex h-14 items-center justify-between gap-4 border-b border-[var(--color-border)] bg-[var(--color-bg)]/85 px-6 backdrop-blur">
      <div className="flex min-w-0 items-center gap-3">{breadcrumb}</div>
      <div className="flex items-center gap-1">
        <ThemeToggle />
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
                <div className="truncate font-mono text-[11px] text-[var(--color-fg-muted)]">
                  {claims?.tenant_id ?? "—"}
                </div>
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
      </div>
    </header>
  );
}

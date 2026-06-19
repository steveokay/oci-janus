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

  const username = claims?.username ?? "User";
  const initial = (username[0] ?? "U").toUpperCase();

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
              <span className="hidden text-sm md:inline">{username}</span>
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
                <div className="text-sm font-medium">{username}</div>
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

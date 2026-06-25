import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { AxiosError } from "axios";
import { toast } from "sonner";
import { UserPlus, Users } from "lucide-react";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { InviteUserDialog } from "@/components/tenant-users/invite-user-dialog";
import { DisableUserDialog } from "@/components/tenant-users/disable-user-dialog";
import { ElevateToOrgAdminDialog } from "@/components/tenant-users/elevate-to-org-admin-dialog";
import { TenantUsersTable } from "@/components/tenant-users/tenant-users-table";
import {
  useSetUserDisabled,
  useTenantUsers,
} from "@/lib/api/tenant-users";
import type { TenantUser } from "@/lib/api/tenant-users";
import { useAuthStore } from "@/lib/auth/store";

// FUT-012 Phase C — Tenant-user lifecycle page.
//
// Audience: tenant-admin (scoped to their own tenant via JWT) AND
// platform-admin (covered by the platform marker — they see all
// tenants the JWT routes them into). Phase D-future could add a
// tenant selector dropdown for platform-admin to jump tenants
// without re-issuing JWTs; today the route shows the active tenant.
//
// Layout: header with total + invite button + free-text filter,
// then table.
//
// The route is RBAC-gated by the BFF — non-admins get a 403 surfaced
// as the ErrorState below.

export const Route = createFileRoute("/_authenticated/tenant/users")({
  component: TenantUsersPage,
});

function TenantUsersPage(): React.ReactElement {
  const { data, isLoading, isError, refetch } = useTenantUsers({ page_size: 50 });
  const enable = useSetUserDisabled();
  const claims = useAuthStore((s) => s.claims);
  const selfUserID = claims?.sub;

  const [inviteOpen, setInviteOpen] = React.useState(false);
  const [disableTarget, setDisableTarget] = React.useState<TenantUser | null>(null);
  const [elevateTarget, setElevateTarget] = React.useState<TenantUser | null>(null);
  const [search, setSearch] = React.useState("");

  if (isError) {
    return (
      <div className="space-y-5 p-6">
        <ErrorState
          title="Couldn't load tenant users"
          description="The management API didn't answer or your role doesn't allow access. Retry, or contact a platform admin."
          onRetry={() => void refetch()}
        />
      </div>
    );
  }

  const users = data?.users ?? [];
  const total = data?.total_count ?? 0;
  // Free-text filter — same shape as the proxy-cache + tags surfaces:
  // case-insensitive substring across username, display_name, email.
  const visibleUsers = React.useMemo(() => {
    const q = search.trim().toLowerCase();
    if (q === "") return users;
    return users.filter((u) => {
      const hay = (u.username + " " + u.display_name + " " + u.email).toLowerCase();
      return hay.includes(q);
    });
  }, [users, search]);

  async function handleEnable(u: TenantUser): Promise<void> {
    try {
      const result = await enable.mutateAsync({ user_id: u.user_id, disabled: false });
      toast.success(`Re-enabled @${u.username}. Status is now ${result.status}.`);
    } catch (e) {
      const status = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        status === 403
          ? "Tenant-admin role required."
          : "Re-enable failed. Retry, or check the BFF logs.",
      );
    }
  }

  return (
    <div className="space-y-5 p-6">
      {/* Header */}
      <header className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div>
          <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
            Tenant administration
          </p>
          <h1 className="mt-1 flex items-center gap-2 font-display text-2xl font-medium tracking-tight">
            <Users className="size-5" /> Users
          </h1>
          <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
            {isLoading ? "Loading…" : `${total} ${total === 1 ? "user" : "users"} in this tenant.`}
          </p>
        </div>
        <Button onClick={() => setInviteOpen(true)} disabled={isLoading}>
          <UserPlus className="size-4" /> Invite user
        </Button>
      </header>

      {/* Filter */}
      <div>
        <Label htmlFor="tenant-users-search" className="sr-only">
          Filter tenant users
        </Label>
        <Input
          id="tenant-users-search"
          type="search"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="Filter by username, display name, or email"
          autoComplete="off"
        />
      </div>

      {/* Body */}
      {!isLoading && users.length === 0 ? (
        <EmptyState
          icon={<Users className="size-5" />}
          title="No users yet"
          description="Invite the first colleague with the button above. The recipient gets a one-time invite token they redeem to set their password."
        />
      ) : (
        <TenantUsersTable
          users={visibleUsers}
          loading={isLoading}
          selfUserID={selfUserID}
          onDisable={(u) => setDisableTarget(u)}
          onEnable={(u) => void handleEnable(u)}
          onElevate={(u) => setElevateTarget(u)}
        />
      )}

      {/* Dialogs */}
      <InviteUserDialog open={inviteOpen} onOpenChange={setInviteOpen} />
      <DisableUserDialog
        open={disableTarget !== null}
        onOpenChange={(o) => !o && setDisableTarget(null)}
        user={disableTarget}
      />
      <ElevateToOrgAdminDialog
        open={elevateTarget !== null}
        onOpenChange={(o) => !o && setElevateTarget(null)}
        user={elevateTarget}
      />
    </div>
  );
}

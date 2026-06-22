import * as React from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { ChevronRight, Settings, UserPlus, Users } from "lucide-react";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { MembersTable } from "@/components/members/members-table";
import { AddMemberDialog } from "@/components/members/add-member-dialog";
import { RemoveMemberDialog } from "@/components/members/remove-member-dialog";
import {
  useGrantOrgRole,
  useOrgMembers,
  useRevokeOrgRole,
  type Member,
} from "@/lib/api/members";

export const Route = createFileRoute("/_authenticated/orgs/$org/members")({
  component: OrgMembers,
});

function OrgMembers(): React.ReactElement {
  const { org } = Route.useParams();
  const { data, isLoading, isError, refetch } = useOrgMembers(org);
  const grant = useGrantOrgRole(org);
  const revoke = useRevokeOrgRole(org);

  const [addOpen, setAddOpen] = React.useState(false);
  const [pendingRemove, setPendingRemove] = React.useState<Member | null>(null);

  return (
    <div className="space-y-6">
      <nav
        aria-label="Breadcrumb"
        className="flex items-center gap-1 text-xs text-[var(--color-fg-muted)]"
      >
        <Link to="/members" className="hover:text-[var(--color-fg)]">
          Members
        </Link>
        <ChevronRight className="size-3 text-[var(--color-fg-subtle)]" />
        <span className="font-mono text-[var(--color-fg)]">{org}</span>
      </nav>

      <header className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
            Organization
          </p>
          <h1 className="font-display text-3xl font-medium tracking-tight">
            <span className="text-[var(--color-fg-muted)]">org/</span>
            {org}
          </h1>
          <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
            {isLoading
              ? "Loading members…"
              : `${data?.length ?? 0} ${(data?.length ?? 0) === 1 ? "member" : "members"} with role assignments`}
          </p>
        </div>
        <div className="flex items-center gap-2">
          {/* Cross-link to /orgs/$org/settings — slice 4 lands the */}
          {/* Default retention surface there. Kept as a separate route */}
          {/* (not a tab) so each surface is URL-honest.                 */}
          <Button asChild variant="ghost" size="sm">
            <Link to="/orgs/$org/settings" params={{ org }}>
              <Settings className="size-4" />
              Org settings
            </Link>
          </Button>
          <Button onClick={() => setAddOpen(true)}>
            <UserPlus className="size-4" />
            Add member
          </Button>
        </div>
      </header>

      {isError ? (
        <ErrorState
          title="Couldn't load members"
          description={`We weren't able to fetch members for ${org}. Retry, or check the BFF logs.`}
          onRetry={() => void refetch()}
        />
      ) : !isLoading && (data?.length ?? 0) === 0 ? (
        <EmptyState
          icon={<Users className="size-5" />}
          title={`No members on ${org} yet`}
          description="Grant a role to bring teammates or robot accounts into this organization."
          action={
            <Button onClick={() => setAddOpen(true)}>
              <UserPlus className="size-4" />
              Add first member
            </Button>
          }
        />
      ) : (
        <MembersTable
          members={data ?? []}
          loading={isLoading}
          onRemove={(m) => setPendingRemove(m)}
        />
      )}

      <AddMemberDialog
        open={addOpen}
        onOpenChange={setAddOpen}
        scopeLabel={org}
        onGrant={async (body) => {
          await grant.mutateAsync(body);
        }}
      />

      <RemoveMemberDialog
        open={pendingRemove !== null}
        onOpenChange={(o) => {
          if (!o) setPendingRemove(null);
        }}
        member={pendingRemove}
        scopeLabel={org}
        onRevoke={async (id) => {
          await revoke.mutateAsync(id);
        }}
      />
    </div>
  );
}

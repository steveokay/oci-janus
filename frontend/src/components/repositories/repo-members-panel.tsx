import * as React from "react";
import { UserPlus, Users } from "lucide-react";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { MembersTable } from "@/components/members/members-table";
import { AddMemberDialog } from "@/components/members/add-member-dialog";
import { RemoveMemberDialog } from "@/components/members/remove-member-dialog";
import {
  useGrantRepoRole,
  useRepoMembers,
  useRevokeRepoRole,
  type Member,
} from "@/lib/api/members";

interface RepoMembersPanelProps {
  org: string;
  repo: string;
}

// Beacon — RepoMembersPanel. Drop-in for the Members tab on the repo
// detail page. Reuses every member primitive — only the underlying scope
// + hooks change vs. the org-members route.
export function RepoMembersPanel({
  org,
  repo,
}: RepoMembersPanelProps): React.ReactElement {
  const { data, isLoading, isError, refetch } = useRepoMembers(org, repo);
  const grant = useGrantRepoRole(org, repo);
  const revoke = useRevokeRepoRole(org, repo);

  const [addOpen, setAddOpen] = React.useState(false);
  const [pendingRemove, setPendingRemove] = React.useState<Member | null>(null);

  const scopeLabel = `${org}/${repo}`;

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <p className="text-sm text-[var(--color-fg-muted)]">
          Role assignments on this repository override the parent org for the
          listed users.
        </p>
        <Button onClick={() => setAddOpen(true)} size="sm">
          <UserPlus className="size-4" />
          Add member
        </Button>
      </div>

      {isError ? (
        <ErrorState
          title="Couldn't load members"
          description="The members endpoint didn't answer. Retry, or check the BFF logs."
          onRetry={() => void refetch()}
        />
      ) : !isLoading && (data?.length ?? 0) === 0 ? (
        <EmptyState
          icon={<Users className="size-5" />}
          title="No repo-specific members"
          description="Anyone with an org-level role inherits access here. Add a repo grant only when you need to override or scope tighter."
          action={
            <Button onClick={() => setAddOpen(true)} size="sm">
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
        scopeLabel={scopeLabel}
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
        scopeLabel={scopeLabel}
        onRevoke={async (id) => {
          await revoke.mutateAsync(id);
        }}
      />
    </div>
  );
}

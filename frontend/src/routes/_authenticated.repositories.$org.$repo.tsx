import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useRepository } from "@/lib/api/repositories";
import { RepositoryHeader } from "@/components/repositories/repository-header";
import { PullCommandCard } from "@/components/repositories/pull-command-card";
import { TagsPanel } from "@/components/repositories/tags-panel";
import { DeleteRepositoryDialog } from "@/components/repositories/delete-repository-dialog";
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "@/components/ui/tabs";
import { ErrorState } from "@/components/ui/error-state";
import { EmptyState } from "@/components/ui/empty-state";
import { Users, Settings } from "lucide-react";

export const Route = createFileRoute("/_authenticated/repositories/$org/$repo")({
  component: RepositoryDetail,
});

function RepositoryDetail(): React.ReactElement {
  const { org, repo } = Route.useParams();
  const { data, isLoading, isError, refetch } = useRepository(org, repo);
  const [deleteOpen, setDeleteOpen] = React.useState(false);

  if (isError) {
    return (
      <ErrorState
        title="Couldn't load repository"
        description={`We weren't able to fetch ${org}/${repo}. It may have been deleted, or you may not have access.`}
        onRetry={() => void refetch()}
      />
    );
  }

  return (
    <div className="space-y-8">
      <RepositoryHeader
        repo={data}
        loading={isLoading}
        onDelete={() => setDeleteOpen(true)}
      />

      <PullCommandCard org={org} repo={repo} />

      <Tabs defaultValue="tags">
        <TabsList>
          <TabsTrigger value="tags">Tags</TabsTrigger>
          <TabsTrigger value="members">Members</TabsTrigger>
          <TabsTrigger value="settings">Settings</TabsTrigger>
        </TabsList>

        <TabsContent value="tags">
          <TagsPanel org={org} repo={repo} />
        </TabsContent>

        <TabsContent value="members">
          <EmptyState
            icon={<Users className="size-5" />}
            title="Repository membership wires up in Sprint 4"
            description="Per-repo role grants (owner / admin / writer / reader) live in the RBAC sprint. Org membership already works under Members in the sidebar."
          />
        </TabsContent>

        <TabsContent value="settings">
          <EmptyState
            icon={<Settings className="size-5" />}
            title="Repository settings arrive in Sprint 4"
            description="Quota override, description / README, and visibility toggle land next to the membership work."
          />
        </TabsContent>
      </Tabs>

      {data ? (
        <DeleteRepositoryDialog
          open={deleteOpen}
          onOpenChange={setDeleteOpen}
          repo={data}
        />
      ) : null}
    </div>
  );
}

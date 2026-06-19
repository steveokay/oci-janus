import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useRepository } from "@/lib/api/repositories";
import { RepositoryHeader } from "@/components/repositories/repository-header";
import { PullCommandCard } from "@/components/repositories/pull-command-card";
import { TagsPanel } from "@/components/repositories/tags-panel";
import { DeleteRepositoryDialog } from "@/components/repositories/delete-repository-dialog";
import { RepoMembersPanel } from "@/components/repositories/repo-members-panel";
import { DescriptionCard } from "@/components/repositories/description-card";
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "@/components/ui/tabs";
import { ErrorState } from "@/components/ui/error-state";
import { EmptyState } from "@/components/ui/empty-state";
import { Settings } from "lucide-react";

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

      <DescriptionCard description={data?.description} />

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
          <RepoMembersPanel org={org} repo={repo} />
        </TabsContent>

        <TabsContent value="settings">
          <EmptyState
            icon={<Settings className="size-5" />}
            title="Repository settings arrive in a later sprint"
            description="Quota override, description editing, and visibility toggle land alongside the per-tenant policy editor."
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

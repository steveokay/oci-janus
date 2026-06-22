import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useRepository } from "@/lib/api/repositories";
import { RepositoryHeader } from "@/components/repositories/repository-header";
import { PullCommandCard } from "@/components/repositories/pull-command-card";
import { TagsPanel } from "@/components/repositories/tags-panel";
import { DeleteRepositoryDialog } from "@/components/repositories/delete-repository-dialog";
import { RepoMembersPanel } from "@/components/repositories/repo-members-panel";
import { DescriptionCard } from "@/components/repositories/description-card";
import { RetentionPanel } from "@/components/repositories/retention-panel";
import { RepoScanPolicySection } from "@/components/repositories/repo-scan-policy-section";
import { AnalyticsCard } from "@/components/dashboard/analytics-card";
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

      <AnalyticsCard scope="repo" org={org} repo={repo} />

      <Tabs defaultValue="tags">
        <TabsList>
          <TabsTrigger value="tags">Tags</TabsTrigger>
          <TabsTrigger value="members">Members</TabsTrigger>
          {/* S11 Slice 1 — Retention tab sits between Members and Settings */}
          {/* so the destructive primitives (members, retention, future       */}
          {/* delete-repo) cluster together in the rightmost positions.      */}
          <TabsTrigger value="retention">Retention</TabsTrigger>
          <TabsTrigger value="settings">Settings</TabsTrigger>
        </TabsList>

        <TabsContent value="tags">
          <TagsPanel org={org} repo={repo} />
        </TabsContent>

        <TabsContent value="members">
          <RepoMembersPanel org={org} repo={repo} />
        </TabsContent>

        <TabsContent value="retention">
          <RetentionPanel org={org} repo={repo} />
        </TabsContent>

        <TabsContent value="settings" className="space-y-4">
          {/* FE-API-049 + 050 polish — per-repo scan policy editor. */}
          {/* Other settings (quota override, description edit, etc.) */}
          {/* land here in future sprints alongside their backend     */}
          {/* surfaces.                                                */}
          <RepoScanPolicySection org={org} repo={repo} />
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

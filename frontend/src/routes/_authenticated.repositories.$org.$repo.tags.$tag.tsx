import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { toast } from "sonner";
import { useTags } from "@/lib/api/tags";
import { useScan, useTriggerScan } from "@/lib/api/scan";
import { useBuilds } from "@/lib/api/builds";
import { TagHeader } from "@/components/tags/tag-header";
import { PullCommandCard } from "@/components/repositories/pull-command-card";
import { ScanPanel } from "@/components/security/scan-panel";
import { BuildTimeline } from "@/components/builds/build-timeline";
import { DeleteTagDialog } from "@/components/tags/delete-tag-dialog";
import { LayersPanel } from "@/components/tags/layers-panel";
import { SigningPanel } from "@/components/tags/signing-panel";
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "@/components/ui/tabs";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";

export const Route = createFileRoute(
  "/_authenticated/repositories/$org/$repo/tags/$tag",
)({
  component: TagDetail,
});

function TagDetail(): React.ReactElement {
  const { org, repo, tag } = Route.useParams();
  const [deleteOpen, setDeleteOpen] = React.useState(false);

  // No per-tag GET endpoint exists yet; we read from the tag list and pick
  // the row by name. This is fine for the page sizes we expect and the list
  // is already cached by useTags from the repo detail page.
  const {
    data: tags,
    isLoading: tagsLoading,
    isError: tagsError,
    refetch: refetchTags,
  } = useTags(org, repo);
  const tagRow = React.useMemo(
    () => tags?.find((t) => t.name === tag),
    [tags, tag],
  );

  const {
    data: scan,
    isLoading: scanLoading,
    isError: scanError,
    refetch: refetchScan,
  } = useScan(org, repo, tag);

  const {
    data: builds,
    isLoading: buildsLoading,
    isError: buildsError,
    refetch: refetchBuilds,
  } = useBuilds(org, repo, tag);

  const triggerScan = useTriggerScan();
  const scanRunning =
    scan?.status === "pending" || scan?.status === "running" || triggerScan.isPending;

  async function handleRescan(): Promise<void> {
    try {
      await triggerScan.mutateAsync({ org, repo, tag });
      toast.success("Scan queued.");
    } catch {
      toast.error("Couldn't queue scan. Try again in a moment.");
    }
  }

  // Tag list resolved but the tag itself doesn't exist — the operator
  // navigated to an old URL or somebody else deleted it. Tell them clearly.
  if (!tagsLoading && tags && !tagRow) {
    if (tagsError) {
      return (
        <ErrorState
          title="Couldn't load tag"
          description={`We weren't able to load ${tag}. Try again, or head back to ${org}/${repo}.`}
          onRetry={() => void refetchTags()}
        />
      );
    }
    return (
      <EmptyState
        title="Tag not found"
        description={`There's no tag named ${tag} in ${org}/${repo} right now. It may have been deleted, or never existed under that name.`}
      />
    );
  }

  return (
    <div className="space-y-8">
      <TagHeader
        org={org}
        repo={repo}
        tagName={tag}
        tag={tagRow}
        loading={tagsLoading}
        scanRunning={scanRunning}
        onRescan={() => void handleRescan()}
        onDelete={() => setDeleteOpen(true)}
      />

      <PullCommandCard org={org} repo={repo} tag={tag} />

      <Tabs defaultValue="security">
        <TabsList>
          <TabsTrigger value="security">Security</TabsTrigger>
          <TabsTrigger value="history">Push history</TabsTrigger>
          <TabsTrigger value="layers">Layers</TabsTrigger>
          <TabsTrigger value="signing">Signing</TabsTrigger>
        </TabsList>

        <TabsContent value="security">
          <ScanPanel
            scan={scan}
            loading={scanLoading}
            isError={scanError}
            triggering={triggerScan.isPending}
            onTrigger={() => void handleRescan()}
            onRetry={() => void refetchScan()}
          />
        </TabsContent>

        <TabsContent value="history">
          <BuildTimeline
            builds={builds?.builds}
            loading={buildsLoading}
            isError={buildsError}
            onRetry={() => void refetchBuilds()}
          />
        </TabsContent>

        <TabsContent value="layers">
          <LayersPanel org={org} repo={repo} tag={tag} />
        </TabsContent>

        <TabsContent value="signing">
          <SigningPanel org={org} repo={repo} tag={tag} />
        </TabsContent>
      </Tabs>

      <DeleteTagDialog
        open={deleteOpen}
        onOpenChange={setDeleteOpen}
        org={org}
        repo={repo}
        tag={tag}
      />
    </div>
  );
}

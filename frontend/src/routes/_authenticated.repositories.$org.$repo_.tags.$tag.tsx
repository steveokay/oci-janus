import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { toast } from "sonner";
import { useTags } from "@/lib/api/tags";
import { useScan, useTriggerScan } from "@/lib/api/scan";
import { useBuilds } from "@/lib/api/builds";
import { TagHeader } from "@/components/tags/tag-header";
import { PullCommandCard } from "@/components/repositories/pull-command-card";
import { ScanPanel } from "@/components/security/scan-panel";
import { QuarantineBanner } from "@/components/security/quarantine-banner";
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

// Tab values for the tag-detail page. The default landing tab is "security"
// — it's the most informative surface when a scan exists, and the empty
// state for an unscanned tag offers an inline "Other views" affordance
// (DSGN-019) so the operator can hop to a sibling tab without bouncing.
const TAG_TAB_VALUES = ["security", "history", "layers", "signing"] as const;
type TagDetailTab = (typeof TAG_TAB_VALUES)[number];
const DEFAULT_TAG_TAB: TagDetailTab = "security";

interface TagDetailSearch {
  tab?: TagDetailTab;
}

export const Route = createFileRoute(
  "/_authenticated/repositories/$org/$repo_/tags/$tag",
)({
  component: TagDetail,
  // Persist the selected tab in the URL so it survives refresh/back and is
  // deep-linkable. Invalid/absent → default tab. Whitelist mirrors the
  // <TabsTrigger value=…> set so an unknown tab can't reach the controlled Tabs.
  validateSearch: (raw: Record<string, unknown>): TagDetailSearch => {
    const tab = raw.tab;
    if (typeof tab === "string" && (TAG_TAB_VALUES as readonly string[]).includes(tab)) {
      return { tab: tab as TagDetailTab };
    }
    return {};
  },
});

function TagDetail(): React.ReactElement {
  const { org, repo, tag } = Route.useParams();
  const { tab } = Route.useSearch();
  const navigate = Route.useNavigate();
  const [deleteOpen, setDeleteOpen] = React.useState(false);
  // The URL is the single source of truth for the active tab; absent/invalid
  // resolves to the default. setActiveTab navigates (replace:true) so the
  // empty-state sibling-tab links (DSGN-019) drive the same URL param instead
  // of local state — keeping deep-links and the tab bar in sync.
  const activeTab: TagDetailTab = tab ?? DEFAULT_TAG_TAB;
  const setActiveTab = React.useCallback(
    (value: TagDetailTab): void => {
      void navigate({
        search: (prev) => ({ ...prev, tab: value }),
        replace: true,
      });
    },
    [navigate],
  );

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

      <Tabs
        value={activeTab}
        onValueChange={(v) => setActiveTab(v as TagDetailTab)}
      >
        <TabsList>
          <TabsTrigger value="security">Security</TabsTrigger>
          <TabsTrigger value="history">Push history</TabsTrigger>
          <TabsTrigger value="layers">Layers</TabsTrigger>
          <TabsTrigger value="signing">Signing</TabsTrigger>
        </TabsList>

        <TabsContent value="security" className="space-y-4">
          {/* FE-API-050 — renders only when the parent manifest is */}
          {/* quarantined; otherwise this is a no-op. Reads the      */}
          {/* manifest detail via useManifest (cached by other panels). */}
          <QuarantineBanner org={org} repo={repo} tag={tag} />
          <ScanPanel
            scan={scan}
            loading={scanLoading}
            isError={scanError}
            triggering={triggerScan.isPending}
            onTrigger={() => void handleRescan()}
            onRetry={() => void refetchScan()}
            // DSGN-019 — empty-state offers inline links to sibling tabs.
            onSwitchTab={(value) => setActiveTab(value)}
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

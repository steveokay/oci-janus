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
import { RepoImmutabilitySection } from "@/components/repositories/repo-immutability-section";
import { RepoSignaturePolicySection } from "@/components/repositories/repo-signature-policy-section";
import { RepoTrustedKeysSection } from "@/components/repositories/repo-trusted-keys-section";
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

// F4 follow-up — accept an optional ?type=<artifact-type> search param so
// that landing on a repo detail page from /helm pre-applies the Helm chip
// filter on the Tags panel (and likewise /repositories → image). Deep
// links survive too: pasting /repositories/org/repo?type=helm into the
// address bar starts the Tags panel scoped to charts. validateSearch
// rejects unknown values so the URL can't smuggle arbitrary strings into
// the chip filter state.
const ARTIFACT_FILTER_VALUES = [
  "all",
  "image",
  "helm",
  "signature",
  "sbom",
  "other",
] as const;
type ArtifactFilterParam = (typeof ARTIFACT_FILTER_VALUES)[number];

interface RepoDetailSearch {
  type?: ArtifactFilterParam;
}

export const Route = createFileRoute("/_authenticated/repositories/$org/$repo")({
  component: RepositoryDetail,
  validateSearch: (raw: Record<string, unknown>): RepoDetailSearch => {
    const t = raw.type;
    if (typeof t === "string" && (ARTIFACT_FILTER_VALUES as readonly string[]).includes(t)) {
      return { type: t as ArtifactFilterParam };
    }
    return {};
  },
});

function RepositoryDetail(): React.ReactElement {
  const { org, repo } = Route.useParams();
  const { type: initialTypeFilter } = Route.useSearch();
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

      <PullCommandCard
        org={org}
        repo={repo}
        artifactType={initialTypeFilter}
      />

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
          <TagsPanel org={org} repo={repo} initialFilter={initialTypeFilter} />
        </TabsContent>

        <TabsContent value="members">
          <RepoMembersPanel org={org} repo={repo} />
        </TabsContent>

        <TabsContent value="retention">
          <RetentionPanel org={org} repo={repo} />
        </TabsContent>

        <TabsContent value="settings" className="space-y-4">
          {/* Futures.md Tier 1 #2 — tag immutability toggle. Lives    */}
          {/* above the scan-policy section because the security       */}
          {/* posture (rejecting tag re-pushes) is a foundational       */}
          {/* repo-shape decision the operator makes before tuning      */}
          {/* policies.                                                 */}
          <RepoImmutabilitySection org={org} repo={repo} />
          {/* Futures.md Tier 1 #3 — signed-image admission toggle.     */}
          {/* Sits next to immutability because both are security flags  */}
          {/* with the same shape; they compose independently (signed +  */}
          {/* immutable, signed + mutable, etc.) so neither belongs      */}
          {/* "inside" the other.                                        */}
          <RepoSignaturePolicySection org={org} repo={repo} />
          {/* Futures.md Tier 1 #3 Phase 2 — per-repo trusted-key      */}
          {/* allowlist. Sits directly under the policy toggle because */}
          {/* the two compose: the toggle gates pulls on signature     */}
          {/* presence; the allowlist narrows "any signature" down to  */}
          {/* an approved set. Empty allowlist = Phase 1 fallback so   */}
          {/* the cards stay independently useful.                     */}
          <RepoTrustedKeysSection org={org} repo={repo} />
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

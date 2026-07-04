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
import { RepoCVSSPolicySection } from "@/components/repositories/repo-cvss-policy-section";
import { RepoSettingsToc } from "@/components/repositories/repo-settings-toc";
// FUT-020 — image promotion history tab.
import { PromotionsTab } from "@/components/repositories/PromotionsTab";
import { AnalyticsCard } from "@/components/dashboard/analytics-card";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "@/components/ui/tabs";
import { ErrorState } from "@/components/ui/error-state";

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

// Tab values for the repo-detail page — kept in the URL so a selected tab
// survives refresh/back and is deep-linkable (e.g. …/org/repo?tab=settings).
// The list must mirror the <TabsTrigger value=…> set below; validateSearch
// rejects anything else so the URL can't smuggle an unknown tab into the
// controlled <Tabs>.
const REPO_TAB_VALUES = [
  "tags",
  "members",
  "retention",
  "promotions",
  "settings",
] as const;
type RepoTabParam = (typeof REPO_TAB_VALUES)[number];
const DEFAULT_REPO_TAB: RepoTabParam = "tags";

interface RepoDetailSearch {
  type?: ArtifactFilterParam;
  tab?: RepoTabParam;
}

export const Route = createFileRoute("/_authenticated/repositories/$org/$repo")({
  component: RepositoryDetail,
  validateSearch: (raw: Record<string, unknown>): RepoDetailSearch => {
    const out: RepoDetailSearch = {};
    const t = raw.type;
    if (typeof t === "string" && (ARTIFACT_FILTER_VALUES as readonly string[]).includes(t)) {
      out.type = t as ArtifactFilterParam;
    }
    // Only persist a valid, non-default tab — absent/invalid falls through to
    // the default so we don't clutter the URL for the common "tags" case.
    const tab = raw.tab;
    if (typeof tab === "string" && (REPO_TAB_VALUES as readonly string[]).includes(tab)) {
      out.tab = tab as RepoTabParam;
    }
    return out;
  },
});

function RepositoryDetail(): React.ReactElement {
  const { org, repo } = Route.useParams();
  const { type: initialTypeFilter, tab } = Route.useSearch();
  const navigate = Route.useNavigate();
  const { data, isLoading, isError, refetch } = useRepository(org, repo);
  const [deleteOpen, setDeleteOpen] = React.useState(false);

  // Absent/invalid ?tab= resolves to the default tab. The <Tabs> is driven
  // as controlled so the URL is the single source of truth.
  const activeTab: RepoTabParam = tab ?? DEFAULT_REPO_TAB;
  const handleTabChange = React.useCallback(
    (value: string): void => {
      // replace:true so tab switches don't stack history entries. Spread the
      // previous search so sibling params (e.g. ?type=) survive the change.
      void navigate({
        search: (prev) => ({ ...prev, tab: value as RepoTabParam }),
        replace: true,
      });
    },
    [navigate],
  );

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

      {/* Pull/install instructions and Repository activity sit side-by-side */}
      {/* on wide viewports so the operator sees "how do I get this" and    */}
      {/* "what's been happening here" without scrolling. The two cards have */}
      {/* roughly equivalent height (3-step walkthrough vs. analytics       */}
      {/* sparkline) so md:grid-cols-2 keeps the row balanced. Stacks back  */}
      {/* to single-column below md so narrow viewports stay readable.      */}
      <div className="grid gap-4 md:grid-cols-2">
        <PullCommandCard
          org={org}
          repo={repo}
          artifactType={initialTypeFilter}
        />
        <AnalyticsCard scope="repo" org={org} repo={repo} />
      </div>

      <DescriptionCard description={data?.description} />

      <Tabs value={activeTab} onValueChange={handleTabChange}>
        <TabsList>
          <TabsTrigger value="tags">Tags</TabsTrigger>
          <TabsTrigger value="members">Members</TabsTrigger>
          {/* S11 Slice 1 — Retention tab sits between Members and Settings */}
          {/* so the destructive primitives (members, retention, future       */}
          {/* delete-repo) cluster together in the rightmost positions.      */}
          <TabsTrigger value="retention">Retention</TabsTrigger>
          {/* FUT-020 — promotion history. Sits between Retention and       */}
          {/* Settings so read-mostly views cluster together and the        */}
          {/* mutating Settings tab stays in the rightmost position.        */}
          <TabsTrigger value="promotions">Promotions</TabsTrigger>
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

        <TabsContent value="promotions">
          <PromotionsTab org={org} repo={repo} />
        </TabsContent>

        <TabsContent value="settings">
          {/* DSGN-006 — Settings tab was a vertical wall of 5 flat   */}
          {/* cards. We now group by intent: Security (immutability + */}
          {/* signature + trusted keys), Quality (scan policy), and   */}
          {/* General (rename/transfer/description/quota — a          */}
          {/* placeholder slot that future surfaces drop into).       */}
          {/*                                                          */}
          {/* Layout: single column up through lg, then a two-column   */}
          {/* split at xl with a sticky right-side ToC. Card internals */}
          {/* are unchanged; this is layout-only.                      */}
          <div className="grid gap-8 xl:grid-cols-[1fr_12rem]">
            <div className="space-y-10">
              <SettingsSection
                id="security"
                eyebrow="Security"
                description="Repo-wide posture flags. These compose: enabling immutability locks tag movement; requiring signatures gates pulls; the trusted-key allowlist narrows which signatures count."
              >
                {/* Futures.md Tier 1 #2 — tag immutability toggle. Lives    */}
                {/* first because the security posture (rejecting tag       */}
                {/* re-pushes) is a foundational repo-shape decision the    */}
                {/* operator makes before tuning policies.                  */}
                <RepoImmutabilitySection org={org} repo={repo} />
                {/* Futures.md Tier 1 #3 — signed-image admission toggle.   */}
                {/* Sits next to immutability because both are security     */}
                {/* flags with the same shape; they compose independently   */}
                {/* (signed + immutable, signed + mutable, etc.).           */}
                <RepoSignaturePolicySection org={org} repo={repo} />
                {/* FUT-021 — CVSS-gated pull admission. Composes with     */}
                {/* signed-image admission: an operator can require       */}
                {/* signed AND scan-clean images by turning both on.      */}
                {/* Fails OPEN on no-scan-yet so first pulls never break. */}
                <RepoCVSSPolicySection org={org} repo={repo} />
                {/* Futures.md Tier 1 #3 Phase 2 — per-repo trusted-key    */}
                {/* allowlist. Sits directly under the policy toggle       */}
                {/* because the two compose: the toggle gates pulls on     */}
                {/* signature presence; the allowlist narrows "any         */}
                {/* signature" down to an approved set. Empty allowlist =  */}
                {/* Phase 1 fallback so the cards stay independently       */}
                {/* useful.                                                */}
                <RepoTrustedKeysSection org={org} repo={repo} />
              </SettingsSection>

              <SettingsSection
                id="quality"
                eyebrow="Quality"
                description="Vulnerability-scan policy for this repository. Overrides the org / tenant default. Remove the override to inherit again."
              >
                {/* FE-API-049 + 050 polish — per-repo scan policy editor. */}
                <RepoScanPolicySection org={org} repo={repo} />
              </SettingsSection>

              <SettingsSection
                id="general"
                eyebrow="General"
                description="Repository metadata — name, owner, description, quota. Editors for these surfaces land in a later sprint alongside their backend RPCs."
              >
                <RepoGeneralPlaceholder />
              </SettingsSection>
            </div>

            {/* Optional sticky ToC, xl+ only. The component itself     */}
            {/* hides at <xl, so this column collapses cleanly on       */}
            {/* narrower viewports without an empty grid track because  */}
            {/* xl:grid-cols only applies at xl and above.              */}
            <RepoSettingsToc
              items={[
                { id: "security", label: "Security" },
                { id: "quality", label: "Quality" },
                { id: "general", label: "General" },
              ]}
            />
          </div>
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

interface SettingsSectionProps {
  id: string;
  eyebrow: string;
  description: string;
  children: React.ReactNode;
}

// SettingsSection — eyebrow + one-line description wrapper that
// matches the small-caps tracking-[0.18em] pattern used by the rest
// of the dashboard's section headers (audit-export, domains, orgs
// settings). Stacks contained cards with space-y-4 — same rhythm
// the old flat layout had, just nested under a heading now.
//
// The `id` becomes the in-page anchor target for RepoSettingsToc.
// scroll-mt-24 nudges the section down when the user clicks a ToC
// link so the eyebrow isn't tucked under the topbar.
function SettingsSection({
  id,
  eyebrow,
  description,
  children,
}: SettingsSectionProps): React.ReactElement {
  return (
    <section id={id} className="scroll-mt-24 space-y-4">
      <div className="space-y-1">
        <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          {eyebrow}
        </p>
        <p className="text-sm text-[var(--color-fg-muted)]">{description}</p>
      </div>
      <div className="space-y-4">{children}</div>
    </section>
  );
}

// RepoGeneralPlaceholder — slot card for the eventual rename /
// transfer / description / quota CRUD surfaces. Rendering it now
// (rather than leaving "General" empty) makes the three-section
// structure visible on every repo, including freshly-created ones,
// and gives operators a single place to look for "edit metadata"
// affordances once they ship.
function RepoGeneralPlaceholder(): React.ReactElement {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Repository metadata</CardTitle>
        <CardDescription>
          Rename, transfer, description, and quota editors land here in a
          later sprint alongside their backend RPCs.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <ul className="space-y-1 text-sm text-[var(--color-fg-muted)]">
          <li>Rename — change the repo's slug under the same org.</li>
          <li>
            Transfer — move the repo to another org you belong to.
          </li>
          <li>
            Description — long-form README rendered on the repo overview.
          </li>
          <li>
            Quota override — per-repo cap on storage / pull bandwidth.
          </li>
        </ul>
      </CardContent>
    </Card>
  );
}

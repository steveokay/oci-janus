import * as React from "react";
import { Link } from "@tanstack/react-router";
import { toast } from "sonner";
import { AxiosError } from "axios";
import {
  Clock,
  ExternalLink,
  Hourglass,
  Lock,
  Pencil,
  ShieldCheck,
  Trash2,
} from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import {
  describeRule,
  useDeleteRepoRetention,
  useRepoRetention,
  useRepoRetentionPreview,
  type RetentionPolicy,
  type RetentionRule,
} from "@/lib/api/retention";
import { formatAbsoluteDate, formatBytes, formatRelativeDate } from "@/lib/format";
import { cn } from "@/lib/utils";
import { RetentionEditor } from "./retention-editor";
import { RetentionRunCard } from "./retention-run-card";
import { RetentionRunHistoryPanel } from "./retention-run-history";

// Beacon — RetentionPanel (S11 Slice 1, FE-API-037 + FE-API-038).
//
// Read-only render of the per-repo retention policy. Four states:
//
//   1. Loading                — skeleton card
//   2. Error (non-404)        — ErrorState with retry
//   3. No policy anywhere     — empty-state with "Create policy" CTA
//                               (CTA is disabled until slice 2 lands the
//                               editor — surfaced as a muted ghost button)
//   4. Loaded (per-repo)      — summary card listing rules + protected
//                               patterns + updated metadata
//      Loaded (inherited)     — same shape with an inherited-from badge
//                               and "(inherited from org/$org default)"
//                               sub-label so operators see the source
//
// When the loaded policy is inside its 24h preview window
// (preview_until > now), a top-bar banner renders with the countdown
// sourced from GET .../preview so the FE never drifts off clock skew.
//
// Disabled policies (enabled=false) render the same summary with a muted
// "Disabled" pill — the rules are still relevant in slice 2 because the
// editor needs to seed them when the operator flips enabled back on.

interface RetentionPanelProps {
  org: string;
  repo: string;
}

export function RetentionPanel({
  org,
  repo,
}: RetentionPanelProps): React.ReactElement {
  const { policy, notFound, isLoading, isError, refetch } = useRepoRetention(
    org,
    repo,
  );
  // Only fetch preview state once we have a loaded policy. If there's no
  // policy at all the preview endpoint would 404; gating the call here
  // keeps the dev console clean without an extra catch.
  const previewQ = useRepoRetentionPreview(org, repo, !!policy);

  // Editor mode. Distinct from "no policy" — the operator can flip into
  // edit mode from any read state (override, inherited, no-policy).
  // Slice 2 keeps the flag local; later slices can lift it to the route
  // params if deep-linking to the editor is wanted.
  const [editing, setEditing] = React.useState(false);

  if (isError) {
    return (
      <ErrorState
        title="Couldn't load retention policy"
        description="The retention endpoint didn't answer. Try again, or check the BFF logs."
        onRetry={refetch}
      />
    );
  }

  if (isLoading) {
    return <RetentionSkeleton />;
  }

  // Editor mode — used for "create" (no policy yet), "edit" (per-repo
  // override exists), and "override default" (inherited policy exists).
  // The editor seeds its initial state from `initial`; passing the
  // inherited row makes "override default" feel like "edit a copy of
  // the org default", which is the operator's natural mental model.
  if (editing) {
    return (
      <RetentionEditor
        org={org}
        repo={repo}
        initial={policy}
        onSaved={() => {
          setEditing(false);
          refetch();
        }}
        onCancel={() => setEditing(false)}
      />
    );
  }

  if (notFound || !policy) {
    return (
      <EmptyState
        icon={<ShieldCheck className="size-5" />}
        title="No retention policy on this repository"
        description={
          "Once you save one, manifests matching the rules below enter a 7-day grace window before they're hard-deleted. " +
          "Protected tag patterns ALWAYS win — they're checked before any rule. " +
          "A dry-run preview shows you what would be deleted before anything lands on the server."
        }
        action={
          <Button onClick={() => setEditing(true)}>Create policy</Button>
        }
      />
    );
  }

  return (
    <div className="space-y-4">
      <PreviewBanner
        policyPreviewUntil={policy.preview_until}
        live={previewQ.data}
      />
      <PolicySummary
        org={org}
        repo={repo}
        policy={policy}
        onEdit={() => setEditing(true)}
      />
      {/* Slice 3 — executor trigger sits below the policy summary so the */}
      {/* operator's eye flow is "what's the policy → run it". Hidden for */}
      {/* inherited rows; running an org-default policy is handled on the */}
      {/* org page in slice 4.                                              */}
      <RetentionRunCard
        org={org}
        repo={repo}
        disabled={policy.inherited_from === "org"}
      />
      {/* REM-013 gap 2 — Run history panel. Backed by the new BFF route */}
      {/* GET .../policies/retention/runs which scopes server-side to    */}
      {/* this repo + the two retention modes. Hidden on inherited       */}
      {/* policies for the same reason RetentionRunCard is.              */}
      <RetentionRunHistoryPanel
        org={org}
        repo={repo}
        disabled={policy.inherited_from === "org"}
      />
    </div>
  );
}

// ── Preview-window banner ──────────────────────────────────────────────────

// PreviewBanner renders the "policy is in preview" callout. Prefers the
// live GET .../preview response over the policy row's stamp so the
// countdown stays accurate; falls back to the row when the preview
// endpoint is loading (slice 1 only renders the panel once the policy GET
// returns, so the row's timestamp is always available as a safety net).
//
// Hidden entirely when:
//   - no preview window is active (no preview_until, or it's in the past)
//   - the live response says in_preview_window=false (server is the
//     source of truth here — clock skew safety)
function PreviewBanner({
  policyPreviewUntil,
  live,
}: {
  policyPreviewUntil: string | undefined;
  live: ReturnType<typeof useRepoRetentionPreview>["data"];
}): React.ReactElement | null {
  const previewUntil = live?.preview_until ?? policyPreviewUntil;
  if (!previewUntil) return null;

  // When live data exists, trust its in_preview_window flag. If it isn't
  // back yet, do a client-side check against the policy row's stamp so
  // the banner appears without waiting for the second round-trip.
  const isPreview = live
    ? live.in_preview_window
    : new Date(previewUntil).getTime() > Date.now();
  if (!isPreview) return null;

  // would_delete totals come straight off the live response. When the
  // live response hasn't arrived yet, hide them — better to show the
  // banner with a countdown only than to render "0 manifests" misleadingly.
  const wouldDelete = live?.would_delete_count;
  const wouldDeleteBytes = live?.would_delete_bytes;

  return (
    <div
      className={cn(
        "rounded-md border border-[var(--color-accent-border)] bg-[var(--color-accent-subtle)]",
        "p-4 text-sm text-[var(--color-fg)]",
      )}
      role="status"
    >
      <div className="flex items-start gap-3">
        <Hourglass
          className="size-4 mt-0.5 shrink-0 text-[var(--color-accent)]"
          aria-hidden
        />
        <div className="space-y-1">
          <div className="font-medium">
            Policy is in preview — no deletions will run yet.
          </div>
          <div className="text-[var(--color-fg-muted)]">
            Preview ends {formatRelativeDate(previewUntil)} (
            {formatAbsoluteDate(previewUntil)}).
            {wouldDelete !== undefined && wouldDelete > 0 ? (
              <>
                {" "}
                Will delete <strong className="font-medium tabular-nums">
                  {wouldDelete.toLocaleString()}
                </strong>{" "}
                {wouldDelete === 1 ? "manifest" : "manifests"}
                {wouldDeleteBytes && wouldDeleteBytes > 0 ? (
                  <> (~{formatBytes(wouldDeleteBytes)})</>
                ) : null}{" "}
                when the window closes.
              </>
            ) : wouldDelete === 0 ? (
              <> Currently no manifests match the policy.</>
            ) : null}
          </div>
        </div>
      </div>
    </div>
  );
}

// ── Loaded policy summary ──────────────────────────────────────────────────

// PolicySummary renders the rules + protected patterns + meta footer.
// Layout deliberately mirrors DescriptionCard / ScanPolicyEditor's read
// mode so the Retention tab feels like part of the same family.
function PolicySummary({
  org,
  repo,
  policy,
  onEdit,
}: {
  org: string;
  repo: string;
  policy: RetentionPolicy;
  onEdit: () => void;
}): React.ReactElement {
  const inherited = policy.inherited_from === "org";
  const disabled = !policy.enabled;
  const del = useDeleteRepoRetention(org, repo);

  // Remove-override only makes sense for a per-repo row; for inherited
  // rows the DELETE would 404 (no per-repo row to remove). Hide the
  // button so the operator never sees a tooltip they can't fix.
  async function onRemoveOverride(): Promise<void> {
    // Confirm in-place rather than via a separate dialog — destructive
    // but reversible (the operator can re-create), and the inheritance
    // safety net means nothing immediately deletes. Lighter than the
    // type-to-confirm pattern used for repo delete.
    if (
      !window.confirm(
        "Remove this per-repo retention override? The repo will fall back to the org default (or have no policy if no default exists). This does not delete any manifests.",
      )
    ) {
      return;
    }
    try {
      await del.mutateAsync();
      toast.success("Per-repo retention override removed.");
    } catch (e) {
      const status = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        status === 403
          ? "Admin or owner on this repo is required to remove retention."
          : status === 404
            ? "Nothing to remove — there was no per-repo override."
            : "Couldn't remove the override. Try again, or check the BFF logs.",
      );
    }
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-center justify-between gap-3">
          <div className="space-y-1">
            <div className="flex items-center gap-2">
              <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
                Retention policy
              </CardDescription>
              {disabled ? (
                <Badge tone="neutral">Disabled</Badge>
              ) : (
                <Badge tone="success" dot>
                  Enabled
                </Badge>
              )}
              {inherited ? (
                <Badge tone="accent">Inherited</Badge>
              ) : null}
            </div>
            {inherited ? (
              <p className="text-xs text-[var(--color-fg-muted)]">
                Inherited from the org default. Override it for this repo by
                creating a per-repo policy — or{" "}
                {/* Contextual entry point to /orgs/$org/settings so an */}
                {/* operator who wants to edit the source default doesn't  */}
                {/* have to navigate via the members page header.          */}
                <Link
                  to="/orgs/$org/settings"
                  params={{ org }}
                  className="inline-flex items-center gap-0.5 text-[var(--color-accent)] hover:underline"
                >
                  edit the org default
                  <ExternalLink className="size-3" aria-hidden />
                </Link>
                .
              </p>
            ) : (
              <p className="text-xs text-[var(--color-fg-muted)]">
                Per-repo override. Removing it falls back to the org default.
              </p>
            )}
          </div>
          <div className="flex items-center gap-2">
            {!inherited ? (
              <Button
                variant="ghost"
                size="sm"
                onClick={onRemoveOverride}
                disabled={del.isPending}
              >
                <Trash2 className="size-3.5" />
                Remove override
              </Button>
            ) : null}
            <Button size="sm" onClick={onEdit}>
              <Pencil className="size-3.5" />
              {inherited ? "Override default" : "Edit"}
            </Button>
          </div>
        </div>
      </CardHeader>

      <CardContent className="space-y-5">
        <RulesList rules={policy.rules} />
        <ProtectedPatterns patterns={policy.protected_tag_patterns} />
        <MetaFooter policy={policy} />
      </CardContent>
    </Card>
  );
}

// RulesList — one row per rule with its operator-readable description and
// a numeric badge. Empty list renders an inline "no rules" hint rather
// than an empty pane so the policy still visibly exists.
function RulesList({
  rules,
}: {
  rules: RetentionRule[];
}): React.ReactElement {
  if (rules.length === 0) {
    return (
      <div className="rounded-md border border-dashed border-[var(--color-border)] p-4 text-sm text-[var(--color-fg-muted)]">
        <Trash2 className="mr-2 inline size-3.5" aria-hidden />
        No rules configured — this policy currently doesn't delete anything.
      </div>
    );
  }

  return (
    <div className="space-y-2">
      <div className="text-[10px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
        Rules
      </div>
      <ul className="divide-y divide-[var(--color-border)] rounded-md border border-[var(--color-border)]">
        {rules.map((rule, i) => (
          <li
            key={`${rule.kind}-${i}`}
            className="flex items-center justify-between gap-3 px-3 py-2.5"
          >
            <div className="flex items-center gap-2 text-sm">
              <Clock
                className="size-3.5 text-[var(--color-fg-subtle)]"
                aria-hidden
              />
              <span>{describeRule(rule)}</span>
              {rule.kind === "max_size_bytes" ? (
                // max_size_bytes value renders as a human-readable byte
                // count — describeRule emits the prefix only so the
                // formatter lives at the call site (avoids importing
                // formatBytes from the API module).
                <span className="font-medium tabular-nums">
                  {formatBytes(rule.value)}
                </span>
              ) : null}
            </div>
          </li>
        ))}
      </ul>
    </div>
  );
}

// ProtectedPatterns — chip row. Hidden entirely when the operator hasn't
// configured any. Patterns are anchored regexes server-side; rendered
// verbatim here so an operator who knows their seed values can spot them.
function ProtectedPatterns({
  patterns,
}: {
  patterns: string[];
}): React.ReactElement | null {
  if (patterns.length === 0) return null;
  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2 text-[10px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
        <Lock className="size-3" aria-hidden />
        Protected tag patterns
      </div>
      <div className="flex flex-wrap gap-1.5">
        {patterns.map((p) => (
          <span
            key={p}
            className="rounded-md border border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] px-2 py-0.5 font-mono text-xs text-[var(--color-fg)]"
          >
            {p}
          </span>
        ))}
      </div>
      <p className="text-[11px] text-[var(--color-fg-subtle)]">
        Tags matching any pattern above are protected even when the rules
        above would otherwise sweep them.
      </p>
    </div>
  );
}

// MetaFooter — updated metadata + tenant context line at the bottom of
// the card. Mirrors the audit footer pattern used on /security pages so
// every "configured by X at Y" line feels the same across the app.
function MetaFooter({
  policy,
}: {
  policy: RetentionPolicy;
}): React.ReactElement {
  return (
    <div className="flex flex-wrap items-center justify-between gap-2 border-t border-[var(--color-border)] pt-3 text-[11px] text-[var(--color-fg-subtle)]">
      <div className="space-x-2">
        {policy.updated_at ? (
          <>
            <span>Updated {formatRelativeDate(policy.updated_at)}</span>
            <span className="text-[var(--color-border-strong)]">·</span>
            <span title={formatAbsoluteDate(policy.updated_at)}>
              {formatAbsoluteDate(policy.updated_at)}
            </span>
          </>
        ) : (
          <span>Updated date not recorded.</span>
        )}
        {policy.updated_by ? (
          <>
            <span className="text-[var(--color-border-strong)]">·</span>
            <span>by {policy.updated_by}</span>
          </>
        ) : null}
      </div>
      <div className="font-mono text-[10px] uppercase tracking-wider">
        {policy.inherited_from === "org" ? "ORG DEFAULT" : "PER-REPO"}
      </div>
    </div>
  );
}

// RetentionSkeleton — geometry matches a loaded policy with two rules so
// the layout doesn't shift when data arrives (S8 polish target). Banner
// row is omitted because the preview banner is only ever shown after the
// real data arrives.
function RetentionSkeleton(): React.ReactElement {
  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="space-y-2">
          <Skeleton className="h-3 w-32" />
          <Skeleton className="h-4 w-64" />
        </div>
      </CardHeader>
      <CardContent className="space-y-5">
        <div className="space-y-2">
          <Skeleton className="h-3 w-16" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
        <Skeleton className="h-6 w-48" />
        <Skeleton className="h-3 w-72" />
      </CardContent>
    </Card>
  );
}

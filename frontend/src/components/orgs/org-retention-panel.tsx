import * as React from "react";
import { toast } from "sonner";
import { AxiosError } from "axios";
import {
  Clock,
  Lock,
  Pencil,
  Plus,
  ShieldCheck,
  Trash2,
  X,
} from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { ConfirmDestructiveDialog } from "@/components/ui/confirm-destructive-dialog";
import {
  describeRule,
  ruleLabel,
  useDeleteOrgRetention,
  useOrgRetention,
  useUpdateOrgRetention,
  type RetentionPolicy,
  type RetentionRule,
  type RetentionRuleKind,
  type UpdateRetentionBody,
} from "@/lib/api/retention";
import {
  formatAbsoluteDate,
  formatBytes,
  formatRelativeDate,
} from "@/lib/format";
import { cn } from "@/lib/utils";

// Beacon — OrgRetentionPanel (S11 Slice 4, FE-API-039).
//
// Org-default retention editor + summary. Lives at /orgs/$org/settings.
// Reuses the visual language of `RetentionPanel` on the repo detail page
// so the two surfaces feel like one feature. Differences from the
// per-repo flow:
//
//   - No dry-run dialog. The BFF doesn't expose a per-org dry-run; the
//     savings projection would have to aggregate across every repo
//     under the org, which is its own backend ticket. Saving goes
//     directly to PUT.
//   - No preview-window banner. The 24h preview state belongs to the
//     per-repo policy (the executor only runs there); org-default
//     changes propagate immediately to inheriting repos.
//   - No run-now button. Triggering retention is repo-scoped.
//
// Save flow:
//   1. Operator clicks "Edit" / "Create org default"
//   2. Editor mounts with seeded values
//   3. "Save policy" → PUT → toast → return to read mode
//   4. Operator can click "Remove default" to clear; inheriting repos
//      revert to "no policy" unless they have their own per-repo
//      override.

interface OrgRetentionPanelProps {
  org: string;
}

export function OrgRetentionPanel({
  org,
}: OrgRetentionPanelProps): React.ReactElement {
  const { policy, notFound, isLoading, isError, refetch } = useOrgRetention(org);
  const [editing, setEditing] = React.useState(false);

  if (isError) {
    return (
      <ErrorState
        title="Couldn't load default retention policy"
        description="The retention endpoint didn't answer. Try again, or check the BFF logs."
        onRetry={refetch}
      />
    );
  }
  if (isLoading) {
    return <PanelSkeleton />;
  }
  if (editing) {
    return (
      <OrgRetentionEditor
        org={org}
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
        title="No default retention policy on this org"
        description={
          "Saving an org default applies to every repository under this org that doesn't have its own override. " +
          "Per-repo policies always win; protected tag patterns are checked before any rule fires."
        }
        action={
          <Button onClick={() => setEditing(true)}>Create org default</Button>
        }
      />
    );
  }
  return (
    <OrgPolicySummary
      org={org}
      policy={policy}
      onEdit={() => setEditing(true)}
    />
  );
}

// ── Summary ────────────────────────────────────────────────────────────────

function OrgPolicySummary({
  org,
  policy,
  onEdit,
}: {
  org: string;
  policy: RetentionPolicy;
  onEdit: () => void;
}): React.ReactElement {
  const del = useDeleteOrgRetention(org);
  const disabled = !policy.enabled;
  const [confirmOpen, setConfirmOpen] = React.useState(false);

  async function onRemoveDefault(): Promise<void> {
    try {
      await del.mutateAsync();
      toast.success("Org-default retention policy removed.");
      setConfirmOpen(false);
    } catch (e) {
      const status = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        status === 403
          ? "Admin or owner on this org is required to remove the default."
          : status === 404
            ? "Nothing to remove — there was no org default."
            : "Couldn't remove the default. Try again, or check the BFF logs.",
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
                Default retention policy
              </CardDescription>
              {disabled ? (
                <Badge tone="neutral">Disabled</Badge>
              ) : (
                <Badge tone="success" dot>
                  Enabled
                </Badge>
              )}
            </div>
            <p className="text-xs text-[var(--color-fg-muted)]">
              Applies to every repository under this org that doesn't have its
              own override.
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setConfirmOpen(true)}
              disabled={del.isPending}
            >
              <Trash2 className="size-3.5" />
              Remove default
            </Button>
            <Button size="sm" onClick={onEdit}>
              <Pencil className="size-3.5" />
              Edit
            </Button>
          </div>
        </div>
      </CardHeader>
      <CardContent className="space-y-5">
        <RulesList rules={policy.rules} />
        <ProtectedPatterns patterns={policy.protected_tag_patterns} />
        <MetaFooter policy={policy} />
      </CardContent>

      <ConfirmDestructiveDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title="Remove org-default retention policy?"
        description="Every repository inheriting it will fall back to 'no policy'. This does not delete any manifests."
        severity="low"
        confirmLabel="Remove default"
        loading={del.isPending}
        onConfirm={onRemoveDefault}
      />
    </Card>
  );
}

// ── Editor ─────────────────────────────────────────────────────────────────

// OrgRetentionEditor — copy of the per-repo editor minus the dry-run
// dialog. Two surfaces diverge slightly today and are likely to diverge
// more as org-level features land (org-wide impact projection, repo
// override coverage), so we keep them as separate components rather than
// over-generalising. The shared building blocks (RulesList,
// ProtectedPatterns, rule kinds list) are duplicated cheaply.

const DEFAULT_PROTECTED_PATTERNS: ReadonlyArray<string> = [
  "latest",
  "stable",
  "^v?\\d+(\\.\\d+){0,2}$",
];

const DEFAULT_NEW_RULE: RetentionRule = {
  kind: "max_age_days",
  value: 30,
};

const RULE_KINDS: ReadonlyArray<{
  kind: RetentionRuleKind;
  unit: string;
  hint: string;
}> = [
  {
    kind: "max_age_days",
    unit: "days",
    hint: "Delete manifests pushed more than N days ago.",
  },
  {
    kind: "max_count",
    unit: "manifests",
    hint: "Keep at most N manifests, deleting the oldest first.",
  },
  {
    kind: "max_size_bytes",
    unit: "bytes",
    hint: "Cap total storage at N bytes (1024 × 1024 = 1 MiB).",
  },
  {
    kind: "dangling_grace_days",
    unit: "days",
    hint: "Sweep untagged manifests after N days.",
  },
  {
    kind: "max_idle_days",
    unit: "days",
    hint: "Delete manifests with no pulls in N days.",
  },
];

function OrgRetentionEditor({
  org,
  initial,
  onSaved,
  onCancel,
}: {
  org: string;
  initial: RetentionPolicy | undefined;
  onSaved: () => void;
  onCancel: () => void;
}): React.ReactElement {
  const seeded = React.useMemo(() => seedForm(initial), [initial]);
  const [enabled, setEnabled] = React.useState(seeded.enabled);
  const [rules, setRules] = React.useState<RetentionRule[]>(seeded.rules);
  const [patterns, setPatterns] = React.useState<string[]>(seeded.patterns);

  const validation = React.useMemo(() => validateRules(rules), [rules]);

  const upd = useUpdateOrgRetention(org);

  async function save(): Promise<void> {
    const body: UpdateRetentionBody = {
      enabled,
      rules,
      protected_tag_patterns: patterns,
    };
    try {
      await upd.mutateAsync(body);
      toast.success("Default retention policy saved.");
      onSaved();
    } catch (e) {
      const status = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        status === 403
          ? "Admin or owner on this org is required to save the default."
          : status === 400
            ? "Backend rejected the policy. Check the rule values."
            : "Couldn't save the default. Try again, or check the BFF logs.",
      );
    }
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          {initial ? "Edit default retention" : "Create default retention"}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-8">
        {/* Enabled */}
        <label className="flex items-center justify-between gap-3 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-4 py-3">
          <div className="min-w-0">
            <div className="text-sm font-medium text-[var(--color-fg)]">
              Enable default retention
            </div>
            <p className="text-xs text-[var(--color-fg-muted)]">
              When disabled, inheriting repos behave as if no default
              exists. Per-repo overrides are unaffected.
            </p>
          </div>
          <Switch
            checked={enabled}
            onCheckedChange={setEnabled}
            aria-label="Enable default retention"
          />
        </label>

        {/* Rules */}
        <Section
          title="Rules"
          description="Each rule independently selects manifests for deletion across every repo inheriting this default."
        >
          <RulesEditor rules={rules} onChange={setRules} />
          {validation.firstError ? (
            <p className="mt-2 text-xs text-[var(--color-danger)]">
              {validation.firstError}
            </p>
          ) : null}
        </Section>

        {/* Protected patterns */}
        <Section
          title="Protected tag patterns"
          description="Manifests matching ANY pattern are spared. Patterns inherit to every repo unless overridden."
        >
          <ProtectedPatternsField patterns={patterns} onChange={setPatterns} />
        </Section>

        {/* Footer */}
        <div className="flex flex-wrap items-center justify-between gap-3 border-t border-[var(--color-border)] pt-4">
          <p className="text-xs text-[var(--color-fg-subtle)]">
            Org-default changes propagate to inheriting repos immediately.
            Per-repo overrides are unaffected.
          </p>
          <div className="flex items-center gap-2">
            <Button variant="ghost" onClick={onCancel} disabled={upd.isPending}>
              Cancel
            </Button>
            <Button
              onClick={save}
              disabled={!validation.ok || upd.isPending}
              loading={upd.isPending}
            >
              {upd.isPending ? "Saving" : "Save policy"}
            </Button>
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

// ── Shared form state helpers ──────────────────────────────────────────────

function seedForm(initial: RetentionPolicy | undefined): {
  enabled: boolean;
  rules: RetentionRule[];
  patterns: string[];
} {
  if (!initial) {
    return {
      enabled: true,
      rules: [DEFAULT_NEW_RULE],
      patterns: [...DEFAULT_PROTECTED_PATTERNS],
    };
  }
  return {
    enabled: initial.enabled,
    rules: initial.rules.map((r) => ({ ...r })),
    patterns: [...initial.protected_tag_patterns],
  };
}

function validateRules(rules: RetentionRule[]): {
  ok: boolean;
  firstError: string | null;
} {
  if (rules.length === 0) return { ok: true, firstError: null };
  for (const r of rules) {
    if (!Number.isFinite(r.value) || r.value <= 0) {
      return {
        ok: false,
        firstError: `Each ${ruleLabel(r.kind)} value must be a positive number.`,
      };
    }
    if (!Number.isInteger(r.value)) {
      return {
        ok: false,
        firstError: `Each ${ruleLabel(r.kind)} value must be a whole number.`,
      };
    }
  }
  return { ok: true, firstError: null };
}

// ── Display sub-components (mirrored from retention-panel.tsx) ─────────────

function Section({
  title,
  description,
  children,
}: {
  title: string;
  description: string;
  children: React.ReactNode;
}): React.ReactElement {
  return (
    <section className="space-y-3">
      <div>
        <div className="text-[10px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          {title}
        </div>
        <p className="mt-0.5 text-xs text-[var(--color-fg-muted)]">
          {description}
        </p>
      </div>
      {children}
    </section>
  );
}

function RulesList({ rules }: { rules: RetentionRule[] }): React.ReactElement {
  if (rules.length === 0) {
    return (
      <div className="rounded-md border border-dashed border-[var(--color-border)] p-4 text-sm text-[var(--color-fg-muted)]">
        <Trash2 className="mr-2 inline size-3.5" aria-hidden />
        No rules configured — this default currently doesn't delete anything.
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
    </div>
  );
}

function MetaFooter({ policy }: { policy: RetentionPolicy }): React.ReactElement {
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
        ORG DEFAULT
      </div>
    </div>
  );
}

// RulesEditor + ProtectedPatternsField — adapted from retention-editor.tsx.
// Same chip UX: select kind, set value, X to remove; ENTER/comma commits
// patterns. Identical to the per-repo flow so an operator who's used one
// already knows the other.

function RulesEditor({
  rules,
  onChange,
}: {
  rules: RetentionRule[];
  onChange: (next: RetentionRule[]) => void;
}): React.ReactElement {
  function updateRule(i: number, patch: Partial<RetentionRule>): void {
    onChange(rules.map((r, idx) => (idx === i ? { ...r, ...patch } : r)));
  }
  function removeRule(i: number): void {
    onChange(rules.filter((_, idx) => idx !== i));
  }
  function addRule(): void {
    const used = new Set(rules.map((r) => r.kind));
    const nextKind =
      RULE_KINDS.find((k) => !used.has(k.kind))?.kind ?? "max_age_days";
    onChange([...rules, { kind: nextKind, value: 30 }]);
  }
  return (
    <div className="space-y-2">
      {rules.length === 0 ? (
        <div className="rounded-md border border-dashed border-[var(--color-border)] p-3 text-xs text-[var(--color-fg-muted)]">
          <Trash2 className="mr-2 inline size-3" aria-hidden />
          No rules yet — saving an empty default is a no-op. Add at least one.
        </div>
      ) : (
        <ul className="space-y-2">
          {rules.map((rule, i) => (
            <li
              key={i}
              className="flex flex-wrap items-center gap-2 rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2"
            >
              <Label htmlFor="" className="sr-only">
                Rule kind
              </Label>
              <select
                value={rule.kind}
                onChange={(e) =>
                  updateRule(i, { kind: e.target.value as RetentionRuleKind })
                }
                className={cn(
                  "h-9 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)]",
                  "px-2 text-xs text-[var(--color-fg)]",
                  "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/40",
                )}
              >
                {RULE_KINDS.map((k) => (
                  <option key={k.kind} value={k.kind}>
                    {ruleLabel(k.kind)}
                  </option>
                ))}
              </select>
              <Input
                type="number"
                inputMode="numeric"
                min={1}
                value={Number.isFinite(rule.value) ? rule.value : ""}
                onChange={(e) => {
                  const n = e.target.valueAsNumber;
                  updateRule(i, { value: Number.isFinite(n) ? n : 0 });
                }}
                className="max-w-[140px] font-mono text-xs tabular-nums"
              />
              <span className="text-xs text-[var(--color-fg-subtle)]">
                {RULE_KINDS.find((k) => k.kind === rule.kind)?.unit ?? ""}
              </span>
              <p className="min-w-[180px] flex-1 text-[11px] text-[var(--color-fg-subtle)]">
                {RULE_KINDS.find((k) => k.kind === rule.kind)?.hint}
              </p>
              <button
                type="button"
                onClick={() => removeRule(i)}
                aria-label={`Remove ${ruleLabel(rule.kind)} rule`}
                className={cn(
                  "grid size-7 place-items-center rounded-md text-[var(--color-fg-muted)]",
                  "hover:bg-[var(--color-surface-sunken)] hover:text-[var(--color-danger)]",
                )}
              >
                <X className="size-3.5" />
              </button>
            </li>
          ))}
        </ul>
      )}
      <Button variant="ghost" size="sm" onClick={addRule}>
        <Plus className="size-3.5" />
        Add rule
      </Button>
    </div>
  );
}

function ProtectedPatternsField({
  patterns,
  onChange,
}: {
  patterns: string[];
  onChange: (next: string[]) => void;
}): React.ReactElement {
  const [draft, setDraft] = React.useState("");
  function commitDraft(): void {
    const candidate = draft.trim();
    if (!candidate) return;
    if (patterns.includes(candidate)) {
      setDraft("");
      return;
    }
    onChange([...patterns, candidate]);
    setDraft("");
  }
  function removeAt(i: number): void {
    onChange(patterns.filter((_, idx) => idx !== i));
  }
  return (
    <div>
      <div className="flex flex-wrap gap-1.5">
        {patterns.length === 0 ? (
          <span className="text-xs italic text-[var(--color-fg-subtle)]">
            No protected patterns — every manifest matching a rule above will
            be marked for deletion.
          </span>
        ) : (
          patterns.map((p, i) => (
            <span
              key={p}
              className="inline-flex items-center gap-1 rounded-md border border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] px-2 py-0.5 font-mono text-[11px] text-[var(--color-fg)]"
            >
              {p}
              <button
                type="button"
                onClick={() => removeAt(i)}
                aria-label={`Remove pattern ${p}`}
                className="grid size-3.5 place-items-center rounded-full text-[var(--color-fg-muted)] hover:bg-[var(--color-fg-muted)]/15"
              >
                <X className="size-2.5" />
              </button>
            </span>
          ))
        )}
      </div>
      <div className="mt-2 flex flex-wrap items-center gap-2">
        <Input
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === ",") {
              e.preventDefault();
              commitDraft();
            } else if (
              e.key === "Backspace" &&
              draft === "" &&
              patterns.length > 0
            ) {
              e.preventDefault();
              removeAt(patterns.length - 1);
            }
          }}
          onBlur={commitDraft}
          placeholder="latest"
          className="max-w-[260px] font-mono text-xs"
          spellCheck={false}
        />
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={commitDraft}
          disabled={!draft.trim()}
        >
          Add pattern
        </Button>
      </div>
    </div>
  );
}

function PanelSkeleton(): React.ReactElement {
  return (
    <Card>
      <CardHeader className="pb-3">
        <Skeleton className="h-4 w-32" />
      </CardHeader>
      <CardContent className="space-y-5">
        <Skeleton className="h-10 w-full" />
        <Skeleton className="h-10 w-full" />
        <Skeleton className="h-6 w-48" />
      </CardContent>
    </Card>
  );
}

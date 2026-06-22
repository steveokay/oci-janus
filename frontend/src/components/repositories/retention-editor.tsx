import * as React from "react";
import { toast } from "sonner";
import { AxiosError } from "axios";
import { Plus, Trash2, X } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import {
  ruleLabel,
  useUpdateRepoRetention,
  type RetentionPolicy,
  type RetentionRule,
  type RetentionRuleKind,
  type UpdateRetentionBody,
} from "@/lib/api/retention";
import { cn } from "@/lib/utils";
import { RetentionDryRunDialog } from "./retention-dry-run-dialog";

// Beacon — RetentionEditor (S11 Slice 2, FE-API-037 + FE-API-038).
//
// Inline editor for the retention policy. Three field groups + a footer:
//
//   - Enabled switch — flipping off keeps the rules but disables the
//     executor (the row stays for next-time re-enable).
//   - Rules — a list of {kind, value} chips. Each chip is a row with a
//     kind select, value input, and an X to remove. "Add rule" picks the
//     next unused kind by default so the operator can spam Add without
//     duplicating.
//   - Protected patterns — chip input, ENTER or comma commits. Pre-seeds
//     with `latest` / `stable` / a semver regex on first creation so a
//     greenfield repo never bombs accidentally.
//
// Save flow:
//   1. Operator clicks "Preview impact"
//   2. RetentionDryRunDialog opens, runs POST .../dry-run with the form
//      values, renders the would-delete table
//   3. Operator clicks "Save policy" inside the dialog → editor's PUT
//      mutation runs → cache seeds + invalidations fire → dialog closes
//      → editor switches back to read mode via onClose
//
// We require Preview before Save on every save (not just first-time) so
// the operator always sees the latest impact relative to current state.
// The "Cancel" button discards local edits without writing anything.

// ── Defaults ───────────────────────────────────────────────────────────────

// Pre-seeded protected patterns the editor adds on first creation. These
// match the seeds the design brief calls out (latest + stable + semver).
// Keeping them client-side rather than seeding server-side means the
// backend never inserts surprise rows — the operator is always in control.
const DEFAULT_PROTECTED_PATTERNS: ReadonlyArray<string> = [
  "latest",
  "stable",
  "^v?\\d+(\\.\\d+){0,2}$",
];

// Pre-seeded sample rule when the operator clicks "Add rule" on a brand
// new policy. Matches the most common operator intent ("delete after
// 30 days") and is easy to override.
const DEFAULT_NEW_RULE: RetentionRule = {
  kind: "max_age_days",
  value: 30,
};

// Allowed rule kinds + helper text. Kind order here also defines the
// "next unused kind" selection order when the operator clicks Add rule
// on a policy that already includes some kinds.
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

// ── Component ──────────────────────────────────────────────────────────────

interface RetentionEditorProps {
  org: string;
  repo: string;
  // The policy this editor mounts against. Pass undefined to render an
  // empty editor (used by the "Create policy" CTA when neither per-repo
  // nor org-default exists).
  initial: RetentionPolicy | undefined;
  // Called once a save completes successfully. The panel uses this to
  // flip back into read mode + refetch the GET.
  onSaved: () => void;
  // Called when the operator cancels. Same return-to-read-mode flow.
  onCancel: () => void;
}

export function RetentionEditor({
  org,
  repo,
  initial,
  onSaved,
  onCancel,
}: RetentionEditorProps): React.ReactElement {
  const seeded = React.useMemo(
    () => seedForm(initial),
    [initial],
  );
  const [enabled, setEnabled] = React.useState(seeded.enabled);
  const [rules, setRules] = React.useState<RetentionRule[]>(seeded.rules);
  const [patterns, setPatterns] = React.useState<string[]>(seeded.patterns);
  const [dialogOpen, setDialogOpen] = React.useState(false);

  // Form-level validation. Allows save only when every rule has a
  // positive integer value and protected patterns are non-empty strings.
  // The BFF re-validates the same set; we mirror the rules client-side
  // so the operator never sees a 400 after clicking Save.
  const validation = React.useMemo(() => validateForm(rules), [rules]);

  const upd = useUpdateRepoRetention(org, repo);

  const candidate: UpdateRetentionBody = React.useMemo(
    () => ({
      enabled,
      rules,
      protected_tag_patterns: patterns,
    }),
    [enabled, rules, patterns],
  );

  async function save(): Promise<void> {
    try {
      await upd.mutateAsync(candidate);
      toast.success("Retention policy saved.");
      onSaved();
    } catch (e) {
      // Re-throw so RetentionDryRunDialog keeps itself open and the
      // operator can either fix the policy or close out manually.
      const status = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        status === 403
          ? "Admin or owner on this repo is required to save retention."
          : status === 400
            ? "Backend rejected the policy. Check the rule values."
            : "Couldn't save the policy. Try again, or check the BFF logs.",
      );
      throw e;
    }
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          {initial?.inherited_from === "org"
            ? "Override the org default for this repository"
            : initial
              ? "Edit retention policy"
              : "Create retention policy"}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-8">
        {/* Enabled switch */}
        <label className="flex items-center justify-between gap-3 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-4 py-3">
          <div className="min-w-0">
            <div className="text-sm font-medium text-[var(--color-fg)]">
              Enable retention
            </div>
            <p className="text-xs text-[var(--color-fg-muted)]">
              When disabled, rules are remembered but the executor never
              marks manifests for deletion.
            </p>
          </div>
          <Switch
            checked={enabled}
            onCheckedChange={setEnabled}
            aria-label="Enable retention"
          />
        </label>

        {/* Rules */}
        <Section
          title="Rules"
          description="Each rule independently selects manifests for deletion. A manifest matching ANY rule is queued for the grace window."
        >
          <RulesEditor rules={rules} onChange={setRules} />
          {validation.firstError ? (
            <p className="mt-2 text-xs text-[var(--color-danger)]">
              {validation.firstError}
            </p>
          ) : null}
        </Section>

        {/* Protected tag patterns */}
        <Section
          title="Protected tag patterns"
          description="Manifests with any tag matching ANY pattern are spared, even when a rule above would otherwise sweep them. Patterns are anchored regexes evaluated server-side."
        >
          <ProtectedPatternsField
            patterns={patterns}
            onChange={setPatterns}
          />
        </Section>

        {/* Footer */}
        <div className="flex flex-wrap items-center justify-between gap-3 border-t border-[var(--color-border)] pt-4">
          <p className="text-xs text-[var(--color-fg-subtle)]">
            Save is gated by a dry-run preview — you always see the impact
            before any policy lands on the server.
          </p>
          <div className="flex items-center gap-2">
            <Button variant="ghost" onClick={onCancel} disabled={upd.isPending}>
              Cancel
            </Button>
            <Button
              onClick={() => setDialogOpen(true)}
              disabled={!validation.ok || upd.isPending}
            >
              Preview impact
            </Button>
          </div>
        </div>
      </CardContent>

      <RetentionDryRunDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        org={org}
        repo={repo}
        candidate={candidate}
        onConfirm={save}
        saving={upd.isPending}
      />
    </Card>
  );
}

// ── Form helpers ───────────────────────────────────────────────────────────

// seedForm — derive the form's initial state. Inherited policies are
// treated as "create" because the per-repo write goes into an empty row,
// but we copy the inherited values so the operator starts from the org
// default instead of from scratch — common UX pattern for "override
// default" flows.
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
    // Defensive copy so the editor's local state doesn't mutate the
    // TanStack cache slice — would re-render every consumer of the
    // query on every chip toggle.
    rules: initial.rules.map((r) => ({ ...r })),
    patterns: [...initial.protected_tag_patterns],
  };
}

// validateForm — mirror the BFF allowlist. Returns the first error
// message so the editor surfaces one issue at a time, matching the rest
// of the app's "fix one thing, save" feedback rhythm.
function validateForm(rules: RetentionRule[]): {
  ok: boolean;
  firstError: string | null;
} {
  if (rules.length === 0) {
    // Empty rules is technically a valid policy — it just deletes
    // nothing. We allow saving it (the disabled-style chip says so) but
    // surface a hint so the operator doesn't accidentally ship a no-op.
    return { ok: true, firstError: null };
  }
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

// ── Sub-components ─────────────────────────────────────────────────────────

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

// RulesEditor — one row per rule with kind select, value input, X. "Add
// rule" picks the next unused kind by default so the operator doesn't
// have to think about duplicates; nothing prevents duplicates (BFF
// accepts them — backend de-dupes) but the default makes the UX clean.
function RulesEditor({
  rules,
  onChange,
}: {
  rules: RetentionRule[];
  onChange: (next: RetentionRule[]) => void;
}): React.ReactElement {
  function updateRule(i: number, patch: Partial<RetentionRule>): void {
    const next = rules.map((r, idx) => (idx === i ? { ...r, ...patch } : r));
    onChange(next);
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
          No rules yet — saving an empty policy is a no-op. Add at least one
          rule below.
        </div>
      ) : (
        <ul className="space-y-2">
          {rules.map((rule, i) => (
            <li
              key={i}
              className="flex flex-wrap items-center gap-2 rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2"
            >
              <RuleKindSelect
                value={rule.kind}
                onChange={(kind) => updateRule(i, { kind })}
              />
              <Input
                type="number"
                inputMode="numeric"
                min={1}
                value={Number.isFinite(rule.value) ? rule.value : ""}
                onChange={(e) => {
                  const n = e.target.valueAsNumber;
                  updateRule(i, {
                    value: Number.isFinite(n) ? n : 0,
                  });
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

// RuleKindSelect — native <select> styled to match the Beacon Input.
// Using native rather than a Radix select to keep the editor light;
// there are only five options and they all fit comfortably in a
// system menu.
function RuleKindSelect({
  value,
  onChange,
}: {
  value: RetentionRuleKind;
  onChange: (next: RetentionRuleKind) => void;
}): React.ReactElement {
  return (
    <>
      <Label htmlFor="" className="sr-only">
        Rule kind
      </Label>
      <select
        value={value}
        onChange={(e) => onChange(e.target.value as RetentionRuleKind)}
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
    </>
  );
}

// ProtectedPatternsField — chip input. Mirrors the ExemptCves pattern
// from scan-policy-editor but accepts free-text strings rather than a
// regex — the backend validates the pattern itself so we don't
// double-implement here. Empty patterns are silently dropped.
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
            No protected patterns — every manifest matching a rule above
            will be marked for deletion.
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

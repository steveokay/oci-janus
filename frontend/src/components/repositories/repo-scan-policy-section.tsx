import * as React from "react";
import { toast } from "sonner";
import { AxiosError } from "axios";
import { Pencil, ShieldCheck, Trash2, X } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import {
  BLOCK_SEVERITY_CHOICES,
  CVE_ID_REGEX,
  SCANNER_PLUGIN_CHOICES,
  useDeleteRepoScanPolicy,
  useRepoScanPolicy,
  useUpdateRepoScanPolicy,
  type BlockOnSeverity,
  type ScannerPlugin,
  type ScopedScanPolicy,
  type UpdateScopedScanPolicyBody,
} from "@/lib/api/scan-policy";
import { formatAbsoluteDate, formatRelativeDate } from "@/lib/format";
import { cn } from "@/lib/utils";

// Beacon — RepoScanPolicySection (FE-API-049 + 050 polish).
//
// Lives on the repo Settings tab. Reads the EFFECTIVE policy (the
// per-repo GET resolves the per-repo → org → tenant → default chain
// server-side and labels the source via inherited_from), so the
// section can render three states:
//
//   - inherited (no per-repo override):
//       Read-only summary of the inherited policy with an "Override
//       default for this repo" CTA that drops into the editor.
//   - per-repo override exists:
//       Read-only summary with Edit + Remove-override affordances.
//       Remove drops back to whatever's inherited from org/tenant.
//   - scanner not wired (404 route disabled):
//       Friendly empty-state pointing operators at the SCANNER_GRPC_ADDR
//       config.
//
// Editor matches OrgScanPolicySection: switch + radio + chip input.
// Duplicated cheaply (the per-tenant /security editor still uses
// react-hook-form + zod; both lighter scopes prefer the same plain
// state-hook style for editor parity).

interface RepoScanPolicySectionProps {
  org: string;
  repo: string;
}

export function RepoScanPolicySection({
  org,
  repo,
}: RepoScanPolicySectionProps): React.ReactElement {
  const { policy, notFound, isLoading, isError, refetch } = useRepoScanPolicy(
    org,
    repo,
  );
  const [editing, setEditing] = React.useState(false);

  if (isError) {
    return (
      <ErrorState
        title="Couldn't load scan policy"
        description="The scanner endpoint didn't answer. Try again, or check the BFF logs."
        onRetry={refetch}
      />
    );
  }
  if (isLoading) return <PanelSkeleton />;
  if (editing) {
    return (
      <RepoScanPolicyEditor
        org={org}
        repo={repo}
        // Seed the editor from whatever policy is currently effective —
        // operator "overriding the org default" starts from the org's
        // values rather than from scratch, which is the common mental
        // model.
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
    // 404 from the BFF means SCANNER_GRPC_ADDR is unset — the route is
    // disabled. Same shape as ScanPolicyEditor's 404 path on /security.
    return (
      <EmptyState
        icon={<ShieldCheck className="size-5" />}
        title="Scan policies aren't wired on this control plane"
        description="Set SCANNER_GRPC_ADDR on the management BFF and restart to enable per-repo policy editing."
      />
    );
  }
  return (
    <PolicySummary
      org={org}
      repo={repo}
      policy={policy}
      onEdit={() => setEditing(true)}
    />
  );
}

// ── Summary ────────────────────────────────────────────────────────────────

function PolicySummary({
  org,
  repo,
  policy,
  onEdit,
}: {
  org: string;
  repo: string;
  policy: ScopedScanPolicy;
  onEdit: () => void;
}): React.ReactElement {
  const del = useDeleteRepoScanPolicy(org, repo);
  const isOverride = policy.inherited_from === "repo";
  const disabled = !policy.enabled;

  async function onRemove(): Promise<void> {
    if (
      !window.confirm(
        "Remove this per-repo scan policy override? The repo falls back to the org default (or the tenant policy if no org default). This does not cancel any in-flight scans or lift any active quarantines.",
      )
    ) {
      return;
    }
    try {
      await del.mutateAsync();
      toast.success("Per-repo scan policy override removed.");
    } catch (e) {
      const status = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        status === 403
          ? "Admin or owner on this repo is required."
          : status === 404
            ? "Nothing to remove — there was no per-repo override."
            : "Couldn't remove the override. Try again, or check the BFF logs.",
      );
    }
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
                Scan policy
              </CardDescription>
              {disabled ? (
                <Badge tone="neutral">Disabled</Badge>
              ) : (
                <Badge tone="success" dot>
                  Enabled
                </Badge>
              )}
              {policy.auto_scan_on_push ? (
                <Badge tone="accent">Auto-scan on push</Badge>
              ) : (
                <Badge tone="warning">Auto-scan disabled</Badge>
              )}
              {isOverride ? null : <InheritedBadge source={policy.inherited_from} />}
            </div>
            <p className="text-xs text-[var(--color-fg-muted)]">
              {isOverride
                ? "Per-repo override. Removing it falls back to whatever this repo would inherit (org default → tenant policy → platform default)."
                : "No per-repo override yet — this repo currently uses the inherited policy. Override it here to give this repo its own settings."}
            </p>
          </div>
          <div className="flex items-center gap-2">
            {isOverride ? (
              <Button
                variant="ghost"
                size="sm"
                onClick={onRemove}
                disabled={del.isPending}
              >
                <Trash2 className="size-3.5" />
                Remove override
              </Button>
            ) : null}
            <Button size="sm" onClick={onEdit}>
              <Pencil className="size-3.5" />
              {isOverride ? "Edit" : "Override default"}
            </Button>
          </div>
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        <SummaryRow
          label="Block-on-severity"
          value={
            policy.block_on_severity === ""
              ? "Never block"
              : policy.block_on_severity
          }
        />
        <SummaryRow label="Scanner backend" value={policy.scanner_plugin} />
        <SummaryRow
          label="Version pin"
          value={policy.scanner_version_pin || "—"}
        />
        <SummaryRow
          label="Exempt CVEs"
          value={
            policy.exempt_cves.length === 0
              ? "none"
              : `${policy.exempt_cves.length} (${policy.exempt_cves
                  .slice(0, 3)
                  .join(", ")}${policy.exempt_cves.length > 3 ? "…" : ""})`
          }
        />
        <div className="flex flex-wrap items-center justify-between gap-2 border-t border-[var(--color-border)] pt-3 text-[11px] text-[var(--color-fg-subtle)]">
          {policy.updated_at ? (
            <span title={formatAbsoluteDate(policy.updated_at)}>
              Updated {formatRelativeDate(policy.updated_at)}
              {policy.updated_by ? ` by ${policy.updated_by}` : null}
            </span>
          ) : (
            <span>Updated date not recorded.</span>
          )}
          <span className="font-mono text-[10px] uppercase tracking-wider">
            {isOverride ? "PER-REPO OVERRIDE" : sourceLabel(policy.inherited_from)}
          </span>
        </div>
      </CardContent>
    </Card>
  );
}

function InheritedBadge({
  source,
}: {
  source?: ScopedScanPolicy["inherited_from"];
}): React.ReactElement | null {
  if (!source || source === "repo") return null;
  return <Badge tone="accent">Inherited · {source}</Badge>;
}

function sourceLabel(source?: ScopedScanPolicy["inherited_from"]): string {
  switch (source) {
    case "org":
      return "INHERITED FROM ORG";
    case "tenant":
      return "INHERITED FROM TENANT";
    case "default":
      return "PLATFORM DEFAULT";
    default:
      return "—";
  }
}

function SummaryRow({
  label,
  value,
}: {
  label: string;
  value: string;
}): React.ReactElement {
  return (
    <div className="flex items-center justify-between gap-2 text-sm">
      <span className="text-[10px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
        {label}
      </span>
      <span className="font-mono text-xs text-[var(--color-fg)]">{value}</span>
    </div>
  );
}

// ── Editor ─────────────────────────────────────────────────────────────────

function RepoScanPolicyEditor({
  org,
  repo,
  initial,
  onSaved,
  onCancel,
}: {
  org: string;
  repo: string;
  initial: ScopedScanPolicy | undefined;
  onSaved: () => void;
  onCancel: () => void;
}): React.ReactElement {
  const seeded = React.useMemo(() => seedForm(initial), [initial]);
  const [enabled, setEnabled] = React.useState(seeded.enabled);
  const [autoScan, setAutoScan] = React.useState(seeded.autoScan);
  const [blockOn, setBlockOn] = React.useState<BlockOnSeverity>(seeded.blockOn);
  const [plugin, setPlugin] = React.useState<ScannerPlugin>(seeded.plugin);
  const [versionPin, setVersionPin] = React.useState(seeded.versionPin);
  const [exemptCves, setExemptCves] = React.useState<string[]>(seeded.exemptCves);

  const upd = useUpdateRepoScanPolicy(org, repo);

  async function save(): Promise<void> {
    const body: UpdateScopedScanPolicyBody = {
      auto_scan_on_push: autoScan,
      block_on_severity: blockOn,
      scanner_plugin: plugin,
      scanner_version_pin: versionPin,
      exempt_cves: exemptCves,
      enabled,
    };
    try {
      await upd.mutateAsync(body);
      toast.success("Per-repo scan policy saved.");
      onSaved();
    } catch (e) {
      const status = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        status === 403
          ? "Admin or owner on this repo is required."
          : status === 400
            ? "Backend rejected the policy — check the field values."
            : "Couldn't save the policy. Try again, or check the BFF logs.",
      );
    }
  }

  const wasOverride = initial?.inherited_from === "repo";

  return (
    <Card>
      <CardHeader className="pb-3">
        <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          {wasOverride
            ? "Edit per-repo scan policy"
            : "Override scan policy for this repo"}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-6">
        <label className="flex items-center justify-between gap-3 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-4 py-3">
          <div className="min-w-0">
            <div className="text-sm font-medium text-[var(--color-fg)]">
              Enable per-repo override
            </div>
            <p className="text-xs text-[var(--color-fg-muted)]">
              When disabled, this repo behaves as if no override exists
              — it inherits from the org default (or tenant fallback).
              Keeps your config without enforcing it.
            </p>
          </div>
          <Switch
            checked={enabled}
            onCheckedChange={setEnabled}
            aria-label="Enable per-repo override"
          />
        </label>

        <label className="flex items-center justify-between gap-3 rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] px-4 py-3">
          <div className="min-w-0">
            <div className="text-sm font-medium text-[var(--color-fg)]">
              Auto-scan on push
            </div>
            <p className="text-xs text-[var(--color-fg-muted)]">
              When enabled, every successful push to this repo triggers
              a scan. Operators can still trigger manual scans either way.
            </p>
          </div>
          <Switch
            checked={autoScan}
            onCheckedChange={setAutoScan}
            aria-label="Auto-scan on push"
          />
        </label>

        <Section
          title="Block-on-severity"
          description="Pushes to this repo are blocked (manifest quarantined post-scan) when findings hit the chosen severity (or worse)."
        >
          <div className="space-y-1.5">
            {BLOCK_SEVERITY_CHOICES.map((choice) => {
              const selected = blockOn === choice.value;
              return (
                <RadioCard
                  key={choice.value || "never"}
                  name="block-on-severity"
                  value={choice.value}
                  checked={selected}
                  onChange={(v) => setBlockOn(v as BlockOnSeverity)}
                  label={choice.label}
                  description={choice.description}
                />
              );
            })}
          </div>
        </Section>

        <Section
          title="Scanner backend"
          description="Override which engine runs against this repo's pushes."
        >
          <div className="grid gap-2 sm:grid-cols-2">
            {SCANNER_PLUGIN_CHOICES.map((choice) => (
              <RadioCard
                key={choice.value}
                name="scanner-plugin"
                value={choice.value}
                checked={plugin === choice.value}
                onChange={(v) => setPlugin(v as ScannerPlugin)}
                label={choice.label}
                description={choice.description}
              />
            ))}
          </div>
          <div className="mt-3">
            <Input
              value={versionPin}
              onChange={(e) => setVersionPin(e.target.value)}
              placeholder="version pin (optional, e.g. 0.52.0)"
              className="font-mono text-xs"
              spellCheck={false}
            />
          </div>
        </Section>

        <Section
          title="Exempt CVEs"
          description="CVE IDs the scanner ignores for the block decision. Useful for accepted-risk findings on this repo."
        >
          <ExemptCvesField value={exemptCves} onChange={setExemptCves} />
        </Section>

        <div className="flex flex-wrap items-center justify-between gap-3 border-t border-[var(--color-border)] pt-4">
          <p className="text-xs text-[var(--color-fg-subtle)]">
            Per-repo overrides ALWAYS win over the org / tenant policy
            for this repository. Take effect on the next push.
          </p>
          <div className="flex items-center gap-2">
            <Button variant="ghost" onClick={onCancel} disabled={upd.isPending}>
              Cancel
            </Button>
            <Button onClick={save} disabled={upd.isPending} loading={upd.isPending}>
              {upd.isPending ? "Saving" : "Save policy"}
            </Button>
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

// ── Helpers (duplicated cheaply from OrgScanPolicySection) ────────────────

function seedForm(initial: ScopedScanPolicy | undefined): {
  enabled: boolean;
  autoScan: boolean;
  blockOn: BlockOnSeverity;
  plugin: ScannerPlugin;
  versionPin: string;
  exemptCves: string[];
} {
  if (!initial) {
    return {
      enabled: true,
      autoScan: true,
      blockOn: "",
      plugin: "trivy",
      versionPin: "",
      exemptCves: [],
    };
  }
  return {
    enabled: initial.enabled,
    autoScan: initial.auto_scan_on_push,
    blockOn: initial.block_on_severity,
    plugin: initial.scanner_plugin,
    versionPin: initial.scanner_version_pin,
    exemptCves: [...initial.exempt_cves],
  };
}

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
    <section className="space-y-2">
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

function RadioCard({
  name,
  value,
  checked,
  onChange,
  label,
  description,
}: {
  name: string;
  value: string;
  checked: boolean;
  onChange: (v: string) => void;
  label: string;
  description: string;
}): React.ReactElement {
  return (
    <label
      className={cn(
        "flex cursor-pointer items-start gap-3 rounded-md border bg-[var(--color-surface)] px-3 py-2 transition-colors",
        checked
          ? "border-[var(--color-accent)] bg-[var(--color-accent-subtle)]/40"
          : "border-[var(--color-border)] hover:bg-[var(--color-surface-sunken)]",
      )}
    >
      <input
        type="radio"
        name={name}
        value={value}
        checked={checked}
        onChange={() => onChange(value)}
        className="sr-only"
      />
      <span
        aria-hidden
        className={cn(
          "mt-0.5 grid size-4 shrink-0 place-items-center rounded-full border",
          checked
            ? "border-[var(--color-accent)]"
            : "border-[var(--color-border-strong)]",
        )}
      >
        {checked ? (
          <span className="size-2 rounded-full bg-[var(--color-accent)]" />
        ) : null}
      </span>
      <span className="min-w-0 flex-1">
        <span className="block text-sm font-medium text-[var(--color-fg)]">
          {label}
        </span>
        <span className="block text-xs text-[var(--color-fg-muted)]">
          {description}
        </span>
      </span>
    </label>
  );
}

function ExemptCvesField({
  value,
  onChange,
}: {
  value: string[];
  onChange: (next: string[]) => void;
}): React.ReactElement {
  const [draft, setDraft] = React.useState("");
  const [error, setError] = React.useState<string | null>(null);

  function commitDraft(): void {
    const candidate = draft.trim().toUpperCase();
    if (!candidate) {
      setError(null);
      return;
    }
    if (!CVE_ID_REGEX.test(candidate)) {
      setError("Use the CVE-YYYY-NNNN format.");
      return;
    }
    if (value.includes(candidate)) {
      setError(null);
      setDraft("");
      return;
    }
    onChange([...value, candidate]);
    setDraft("");
    setError(null);
  }

  return (
    <div>
      <div className="flex flex-wrap gap-1.5">
        {value.length === 0 ? (
          <span className="text-xs italic text-[var(--color-fg-subtle)]">
            No CVEs exempt.
          </span>
        ) : (
          value.map((cve, i) => (
            <span
              key={cve}
              className="inline-flex items-center gap-1 rounded-full border border-[var(--color-accent-border)] bg-[var(--color-accent-subtle)] px-2 py-0.5 font-mono text-[11px] text-[var(--color-accent)]"
            >
              {cve}
              <button
                type="button"
                onClick={() => onChange(value.filter((_, idx) => idx !== i))}
                aria-label={`Remove ${cve}`}
                className="grid size-3.5 place-items-center rounded-full text-[var(--color-accent)] hover:bg-[var(--color-accent)]/15"
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
            if (e.key === "Enter" || e.key === "," || e.key === " ") {
              e.preventDefault();
              commitDraft();
            }
          }}
          onBlur={commitDraft}
          placeholder="CVE-2024-1234"
          className="max-w-[200px] font-mono text-xs"
          spellCheck={false}
        />
      </div>
      {error ? (
        <p className="mt-1.5 text-xs text-[var(--color-danger)]">{error}</p>
      ) : null}
    </div>
  );
}

function PanelSkeleton(): React.ReactElement {
  return (
    <Card>
      <CardHeader className="pb-3">
        <Skeleton className="h-4 w-32" />
      </CardHeader>
      <CardContent className="space-y-4">
        <Skeleton className="h-10 w-full" />
        <Skeleton className="h-10 w-full" />
        <Skeleton className="h-6 w-48" />
      </CardContent>
    </Card>
  );
}

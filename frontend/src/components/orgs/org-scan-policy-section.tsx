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
  useDeleteOrgScanPolicy,
  useOrgScanPolicy,
  useUpdateOrgScanPolicy,
  type BlockOnSeverity,
  type ScannerPlugin,
  type ScopedScanPolicy,
  type UpdateScopedScanPolicyBody,
} from "@/lib/api/scan-policy";
import { formatAbsoluteDate, formatRelativeDate } from "@/lib/format";
import { cn } from "@/lib/utils";

// Beacon — OrgScanPolicySection (FE-API-049).
//
// Sits next to OrgRetentionPanel on /orgs/$org/settings. Three states:
//
//   - loading                 — skeleton
//   - error (non-404)         — ErrorState with retry
//   - no-policy               — empty hint + "Create org default" CTA
//   - loaded                  — summary card + edit/remove affordances
//
// Editor mode toggles inline (no modal). Save → PUT → return to read
// mode + refetch. Remove → DELETE with confirm. Mirrors the
// OrgRetentionPanel pattern so the two surfaces feel like one feature.
//
// The editor's form fields mirror the existing FE-API-018
// ScanPolicyEditor (auto-scan switch + block-on-severity radio group +
// scanner plugin radio + version pin + exempt-CVE chip input) — kept
// inline here rather than extracted because the per-tenant editor lives
// on /security and pulls in form-validation deps that don't fit the
// lighter org settings page. A follow-up could DRY the two editors via
// a shared scope prop; for now the duplication is intentional.

interface OrgScanPolicySectionProps {
  org: string;
}

export function OrgScanPolicySection({
  org,
}: OrgScanPolicySectionProps): React.ReactElement {
  const { policy, notFound, isLoading, isError, refetch } = useOrgScanPolicy(org);
  const [editing, setEditing] = React.useState(false);

  if (isError) {
    return (
      <ErrorState
        title="Couldn't load org scan policy"
        description="The scanner endpoint didn't answer. Try again, or check the BFF logs."
        onRetry={refetch}
      />
    );
  }
  if (isLoading) return <PanelSkeleton />;
  if (editing) {
    return (
      <OrgScanPolicyEditor
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
        title="No org scan policy yet"
        description={
          "Setting an org default applies to every repository under this org that doesn't have its own override. " +
          "Per-repo overrides always win. The platform default — when no org or per-repo row exists — is auto-scan ON with no severity block."
        }
        action={
          <Button onClick={() => setEditing(true)}>Create org default</Button>
        }
      />
    );
  }
  return (
    <ScanPolicySummary
      org={org}
      policy={policy}
      onEdit={() => setEditing(true)}
    />
  );
}

// ── Summary ────────────────────────────────────────────────────────────────

function ScanPolicySummary({
  org,
  policy,
  onEdit,
}: {
  org: string;
  policy: ScopedScanPolicy;
  onEdit: () => void;
}): React.ReactElement {
  const del = useDeleteOrgScanPolicy(org);
  const disabled = !policy.enabled;

  async function onRemove(): Promise<void> {
    if (
      !window.confirm(
        "Remove the org-default scan policy? Every repo inheriting it will fall back to the tenant policy (or the platform default if no tenant row exists). This does not cancel any in-flight scans.",
      )
    ) {
      return;
    }
    try {
      await del.mutateAsync();
      toast.success("Org-default scan policy removed.");
    } catch (e) {
      const status = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        status === 403
          ? "Admin or owner on this org is required."
          : status === 404
            ? "Nothing to remove — there was no org default."
            : "Couldn't remove the policy. Try again, or check the BFF logs.",
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
                Default scan policy
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
            </div>
            <p className="text-xs text-[var(--color-fg-muted)]">
              Applies to every repo under this org without its own override.
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={onRemove}
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
            ORG DEFAULT
          </span>
        </div>
      </CardContent>
    </Card>
  );
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

function OrgScanPolicyEditor({
  org,
  initial,
  onSaved,
  onCancel,
}: {
  org: string;
  initial: ScopedScanPolicy | undefined;
  onSaved: () => void;
  onCancel: () => void;
}): React.ReactElement {
  const seeded = React.useMemo(() => seedForm(initial), [initial]);
  const [enabled, setEnabled] = React.useState(seeded.enabled);
  const [autoScan, setAutoScan] = React.useState(seeded.autoScan);
  const [blockOn, setBlockOn] = React.useState<BlockOnSeverity>(
    seeded.blockOn,
  );
  const [plugin, setPlugin] = React.useState<ScannerPlugin>(seeded.plugin);
  const [versionPin, setVersionPin] = React.useState(seeded.versionPin);
  const [exemptCves, setExemptCves] = React.useState<string[]>(
    seeded.exemptCves,
  );

  const upd = useUpdateOrgScanPolicy(org);

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
      toast.success("Org-default scan policy saved.");
      onSaved();
    } catch (e) {
      const status = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        status === 403
          ? "Admin or owner on this org is required."
          : status === 400
            ? "Backend rejected the policy — check the field values."
            : "Couldn't save the policy. Try again, or check the BFF logs.",
      );
    }
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          {initial ? "Edit org-default scan policy" : "Create org-default scan policy"}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-6">
        <label className="flex items-center justify-between gap-3 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-4 py-3">
          <div className="min-w-0">
            <div className="text-sm font-medium text-[var(--color-fg)]">
              Enable org-default scan policy
            </div>
            <p className="text-xs text-[var(--color-fg-muted)]">
              When disabled, inheriting repos behave as if no default
              exists. Per-repo overrides are unaffected.
            </p>
          </div>
          <Switch
            checked={enabled}
            onCheckedChange={setEnabled}
            aria-label="Enable org-default scan policy"
          />
        </label>

        <label className="flex items-center justify-between gap-3 rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] px-4 py-3">
          <div className="min-w-0">
            <div className="text-sm font-medium text-[var(--color-fg)]">
              Auto-scan on push
            </div>
            <p className="text-xs text-[var(--color-fg-muted)]">
              When enabled, every successful manifest push triggers a scan.
              Operators can still trigger manual scans either way.
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
          description="Pushes are rejected when the scan reveals findings at the chosen severity (or worse)."
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
          description="Picks the engine inheriting repos default to. Per-repo overrides can change this for individual repos."
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
          description="CVE-IDs in this list don't count against the block-on-severity gate. Useful for accepted-risk findings."
        >
          <ExemptCvesField value={exemptCves} onChange={setExemptCves} />
        </Section>

        <div className="flex flex-wrap items-center justify-between gap-3 border-t border-[var(--color-border)] pt-4">
          <p className="text-xs text-[var(--color-fg-subtle)]">
            Saves propagate to inheriting repos on the next push. Per-repo
            overrides are unaffected.
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

// ── Seed + helpers ─────────────────────────────────────────────────────────

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

// Section / RadioCard / ExemptCvesField mirror the existing
// ScanPolicyEditor on /security. Duplicated cheaply (the per-tenant
// editor uses react-hook-form + zod which is overkill for this lighter
// org settings page).

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

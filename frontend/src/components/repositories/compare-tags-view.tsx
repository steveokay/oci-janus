import * as React from "react";
import {
  ArrowRight,
  ArrowUp,
  ArrowDown,
  Plus,
  Minus,
  Layers as LayersIcon,
  Settings2,
  Package as PackageIcon,
  ShieldAlert,
  GitCompare,
} from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Skeleton } from "@/components/ui/skeleton";
import { formatBytes } from "@/lib/format";
import {
  useImageDiff,
  type ImageDiff,
  type ConfigDiff,
  type PackageDiff,
  type VulnDiff,
  type VulnRef,
} from "@/lib/api/diff";

// CompareTagsView — the image-diff surface (Tier 2 #3). Renders the delta
// between two tags of the same repo across four sections: layers, image
// config, packages (from SBOMs), and vulnerabilities (from scans). Each
// non-layer section degrades to a "why it's unavailable" note when the
// backend reports the underlying data is missing (unscanned tag, no SBOM,
// registry-core not wired) — the layer diff is always present.

interface CompareTagsViewProps {
  org: string;
  repo: string;
  from: string;
  to: string;
}

export function CompareTagsView({
  org,
  repo,
  from,
  to,
}: CompareTagsViewProps): React.ReactElement {
  const { data, isLoading, isError, refetch } = useImageDiff(org, repo, from, to);

  if (isError) {
    return (
      <ErrorState
        title="Couldn't load the comparison"
        description="The management API didn't answer. Retry, or check the BFF logs."
        onRetry={() => void refetch()}
      />
    );
  }
  if (isLoading || !data) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-20 w-full rounded-lg" />
        <Skeleton className="h-40 w-full rounded-lg" />
        <Skeleton className="h-40 w-full rounded-lg" />
      </div>
    );
  }

  return (
    <div className="space-y-5">
      <CompareHeader data={data} from={from} to={to} />
      <LayersSection data={data} />
      <ConfigSection config={data.config} />
      <PackagesSection packages={data.packages} />
      <VulnsSection vulns={data.vulnerabilities} />
    </div>
  );
}

// signedBytes renders a size delta with an explicit + / − and a colour cue.
function SignedBytes({ delta }: { delta: number }): React.ReactElement {
  if (delta === 0) {
    return <span className="text-[var(--color-fg-muted)]">no change</span>;
  }
  const grew = delta > 0;
  return (
    <span className={grew ? "text-[var(--color-warning)]" : "text-[var(--color-success)]"}>
      {grew ? "+" : "−"}
      {formatBytes(Math.abs(delta))}
    </span>
  );
}

function shortDigest(d: string): string {
  const hex = d.startsWith("sha256:") ? d.slice(7) : d;
  return hex.slice(0, 12);
}

function CompareHeader({
  data,
  from,
  to,
}: {
  data: ImageDiff;
  from: string;
  to: string;
}): React.ReactElement {
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="flex items-center gap-2 text-base">
          <GitCompare className="size-4 text-[var(--color-accent)]" />
          Comparing tags
        </CardTitle>
        <CardDescription className="flex flex-wrap items-center gap-2 pt-1 font-mono text-xs">
          <span>
            {from}{" "}
            <span className="text-[var(--color-fg-subtle)]">
              @{shortDigest(data.from.digest)}
            </span>
          </span>
          <ArrowRight className="size-3.5 text-[var(--color-fg-subtle)]" aria-hidden />
          <span>
            {to}{" "}
            <span className="text-[var(--color-fg-subtle)]">
              @{shortDigest(data.to.digest)}
            </span>
          </span>
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-wrap gap-x-6 gap-y-1 text-sm">
        <span>
          Total size:{" "}
          <SignedBytes delta={data.layers.size_delta_bytes} />
        </span>
        <span className="text-[var(--color-fg-muted)]">
          {data.from.size_bytes ? formatBytes(data.from.size_bytes) : "—"}
          {" → "}
          {data.to.size_bytes ? formatBytes(data.to.size_bytes) : "—"}
        </span>
      </CardContent>
    </Card>
  );
}

// Section is the shared card wrapper: eyebrow title + icon, body slot.
function Section({
  icon,
  title,
  children,
}: {
  icon: React.ReactNode;
  title: string;
  children: React.ReactNode;
}): React.ReactElement {
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardDescription className="flex items-center gap-2 !text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          {icon}
          {title}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">{children}</CardContent>
    </Card>
  );
}

// AddRemoveRow renders a single added/removed entry with a coloured marker.
function AddRemoveRow({
  kind,
  label,
  sub,
}: {
  kind: "add" | "remove";
  label: string;
  sub?: string;
}): React.ReactElement {
  const add = kind === "add";
  return (
    <div className="flex items-center gap-2 font-mono text-xs">
      {add ? (
        <Plus className="size-3 shrink-0 text-[var(--color-success)]" aria-hidden />
      ) : (
        <Minus className="size-3 shrink-0 text-[var(--color-danger)]" aria-hidden />
      )}
      <span className="truncate">{label}</span>
      {sub ? <span className="text-[var(--color-fg-subtle)]">{sub}</span> : null}
    </div>
  );
}

// VulnRow renders one CVE coloured by good/bad: an introduced CVE is a
// regression (danger + up-arrow), a fixed one is an improvement (success +
// down-arrow) — deliberately NOT the add/remove green/red used elsewhere.
function VulnRow({
  v,
  kind,
}: {
  v: VulnRef;
  kind: "introduced" | "fixed";
}): React.ReactElement {
  const introduced = kind === "introduced";
  const sub = [v.severity, v.package].filter(Boolean).join(" · ");
  return (
    <div
      className={
        "flex items-center gap-2 font-mono text-xs " +
        (introduced ? "text-[var(--color-danger)]" : "text-[var(--color-success)]")
      }
    >
      {introduced ? (
        <ArrowUp className="size-3 shrink-0" aria-hidden />
      ) : (
        <ArrowDown className="size-3 shrink-0" aria-hidden />
      )}
      <span className="truncate">{v.cve}</span>
      {sub ? <span className="text-[var(--color-fg-subtle)]">{sub}</span> : null}
    </div>
  );
}

function LayersSection({ data }: { data: ImageDiff }): React.ReactElement {
  const { added, removed, common_count } = data.layers;
  return (
    <Section icon={<LayersIcon className="size-3.5" />} title="Layers">
      <div className="flex flex-wrap gap-2 text-xs">
        <Badge tone="success">
          <Plus className="size-3" /> {added.length} added
        </Badge>
        <Badge tone="danger">
          <Minus className="size-3" /> {removed.length} removed
        </Badge>
        <Badge tone="neutral">{common_count} shared</Badge>
      </div>
      {added.length === 0 && removed.length === 0 ? (
        <p className="text-xs text-[var(--color-fg-muted)]">
          Identical layer set — the two tags share every layer.
        </p>
      ) : (
        <div className="space-y-1">
          {removed.map((l) => (
            <AddRemoveRow
              key={`r-${l.digest}`}
              kind="remove"
              label={shortDigest(l.digest)}
              sub={formatBytes(l.size)}
            />
          ))}
          {added.map((l) => (
            <AddRemoveRow
              key={`a-${l.digest}`}
              kind="add"
              label={shortDigest(l.digest)}
              sub={formatBytes(l.size)}
            />
          ))}
        </div>
      )}
    </Section>
  );
}

function ConfigSection({ config }: { config: ConfigDiff }): React.ReactElement {
  if (!config.available) {
    return (
      <Section icon={<Settings2 className="size-3.5" />} title="Image config">
        <p className="text-xs text-[var(--color-fg-muted)]">
          {config.reason ?? "Config diff is not available."}
        </p>
      </Section>
    );
  }

  const noChanges =
    config.env.added.length === 0 &&
    config.env.removed.length === 0 &&
    config.env.changed.length === 0 &&
    !config.cmd_changed &&
    !config.entrypoint_changed &&
    config.exposed_ports_added.length === 0 &&
    config.exposed_ports_removed.length === 0 &&
    !config.working_dir_to &&
    !config.user_to;

  return (
    <Section icon={<Settings2 className="size-3.5" />} title="Image config">
      {noChanges ? (
        <p className="text-xs text-[var(--color-fg-muted)]">
          No config differences (ENV, CMD, ENTRYPOINT, ports, workdir, user).
        </p>
      ) : (
        <div className="space-y-2">
          {config.env.changed.map((c) => (
            <div key={`ec-${c.key}`} className="font-mono text-xs">
              <span className="text-[var(--color-fg-subtle)]">ENV</span> {c.key}:{" "}
              <span className="text-[var(--color-danger)]">{c.from || "∅"}</span>
              {" → "}
              <span className="text-[var(--color-success)]">{c.to || "∅"}</span>
            </div>
          ))}
          {config.env.added.map((e) => (
            <AddRemoveRow key={`ea-${e}`} kind="add" label={`ENV ${e}`} />
          ))}
          {config.env.removed.map((e) => (
            <AddRemoveRow key={`er-${e}`} kind="remove" label={`ENV ${e}`} />
          ))}
          {config.cmd_changed ? (
            <div className="font-mono text-xs">
              <span className="text-[var(--color-fg-subtle)]">CMD</span>{" "}
              <span className="text-[var(--color-danger)]">
                {(config.from_cmd ?? []).join(" ") || "∅"}
              </span>
              {" → "}
              <span className="text-[var(--color-success)]">
                {(config.to_cmd ?? []).join(" ") || "∅"}
              </span>
            </div>
          ) : null}
          {config.entrypoint_changed ? (
            <div className="font-mono text-xs">
              <span className="text-[var(--color-fg-subtle)]">ENTRYPOINT</span>{" "}
              <span className="text-[var(--color-danger)]">
                {(config.from_entrypoint ?? []).join(" ") || "∅"}
              </span>
              {" → "}
              <span className="text-[var(--color-success)]">
                {(config.to_entrypoint ?? []).join(" ") || "∅"}
              </span>
            </div>
          ) : null}
          {config.exposed_ports_added.map((p) => (
            <AddRemoveRow key={`pa-${p}`} kind="add" label={`EXPOSE ${p}`} />
          ))}
          {config.exposed_ports_removed.map((p) => (
            <AddRemoveRow key={`pr-${p}`} kind="remove" label={`EXPOSE ${p}`} />
          ))}
          {config.working_dir_to ? (
            <div className="font-mono text-xs">
              <span className="text-[var(--color-fg-subtle)]">WORKDIR</span>{" "}
              {config.working_dir_from || "∅"} → {config.working_dir_to}
            </div>
          ) : null}
          {config.user_to ? (
            <div className="font-mono text-xs">
              <span className="text-[var(--color-fg-subtle)]">USER</span>{" "}
              {config.user_from || "∅"} → {config.user_to}
            </div>
          ) : null}
        </div>
      )}
    </Section>
  );
}

function PackagesSection({
  packages,
}: {
  packages: PackageDiff;
}): React.ReactElement {
  if (!packages.available) {
    return (
      <Section icon={<PackageIcon className="size-3.5" />} title="Packages">
        <p className="text-xs text-[var(--color-fg-muted)]">
          {packages.reason ?? "Package diff is not available."}
        </p>
      </Section>
    );
  }
  const empty =
    packages.added.length === 0 &&
    packages.removed.length === 0 &&
    packages.changed.length === 0;
  return (
    <Section icon={<PackageIcon className="size-3.5" />} title="Packages">
      <div className="flex flex-wrap gap-2 text-xs">
        <Badge tone="success">
          <Plus className="size-3" /> {packages.added.length}
        </Badge>
        <Badge tone="danger">
          <Minus className="size-3" /> {packages.removed.length}
        </Badge>
        <Badge tone="warning">{packages.changed.length} changed</Badge>
      </div>
      {empty ? (
        <p className="text-xs text-[var(--color-fg-muted)]">
          Identical package set.
        </p>
      ) : (
        <div className="space-y-1">
          {packages.changed.map((c) => (
            <div key={`pc-${c.name}`} className="font-mono text-xs">
              {c.name}:{" "}
              <span className="text-[var(--color-danger)]">{c.from_version}</span>
              {" → "}
              <span className="text-[var(--color-success)]">{c.to_version}</span>
            </div>
          ))}
          {packages.added.map((p) => (
            <AddRemoveRow
              key={`pa-${p.name}`}
              kind="add"
              label={p.name}
              sub={p.version}
            />
          ))}
          {packages.removed.map((p) => (
            <AddRemoveRow
              key={`pr-${p.name}`}
              kind="remove"
              label={p.name}
              sub={p.version}
            />
          ))}
        </div>
      )}
    </Section>
  );
}

function VulnsSection({ vulns }: { vulns: VulnDiff }): React.ReactElement {
  if (!vulns.available) {
    return (
      <Section icon={<ShieldAlert className="size-3.5" />} title="Vulnerabilities">
        <p className="text-xs text-[var(--color-fg-muted)]">
          {vulns.reason ?? "Vulnerability diff is not available."}
        </p>
      </Section>
    );
  }
  if (vulns.added.length === 0 && vulns.removed.length === 0) {
    return (
      <Section icon={<ShieldAlert className="size-3.5" />} title="Vulnerabilities">
        <EmptyState
          icon={<ShieldAlert className="size-5" />}
          title="No vulnerability changes"
          description="Both tags carry the same set of known CVEs."
        />
      </Section>
    );
  }
  return (
    <Section icon={<ShieldAlert className="size-3.5" />} title="Vulnerabilities">
      <div className="flex flex-wrap gap-2 text-xs">
        <Badge tone="danger">{vulns.added.length} introduced</Badge>
        <Badge tone="success">{vulns.removed.length} fixed</Badge>
      </div>
      {/* Colour follows good/bad, not add/remove: an introduced CVE is bad
          (danger) even though it's an "addition"; a fixed CVE is good
          (success). Iconography matches — up-arrow = regressed, down = fixed. */}
      <div className="space-y-1">
        {vulns.added.map((v) => (
          <VulnRow key={`va-${v.cve}`} v={v} kind="introduced" />
        ))}
        {vulns.removed.map((v) => (
          <VulnRow key={`vr-${v.cve}`} v={v} kind="fixed" />
        ))}
      </div>
    </Section>
  );
}

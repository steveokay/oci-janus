import * as React from "react";
import { ArrowDownToLine, ArrowUpToLine } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorState } from "@/components/ui/error-state";
import {
  useRepoAnalytics,
  useTenantAnalytics,
  type AnalyticsMetric,
  type AnalyticsRange,
  type AnalyticsResponse,
} from "@/lib/api/analytics";
import { formatCompactNumber } from "@/lib/format";
import { cn } from "@/lib/utils";

// FE-API-030 — pulls / pushes time-series card.
//
// Variants:
//   <AnalyticsCard scope="tenant" />                   — workspace-wide
//   <AnalyticsCard scope="repo" org repo />            — single repository
//
// Renders a metric toggle (pulls / pushes), a range toggle (24h / 7d /
// 30d) and an inline SVG sparkline. SVG rather than a chart lib because
// the points-per-card cap is 30; a 70-line component is cheaper than a
// 200kB dep.

type Scope =
  | { scope: "tenant" }
  | { scope: "repo"; org: string; repo: string };

type AnalyticsCardProps = Scope & {
  className?: string;
  // Initial selection. Operators rarely want pulls (zeroed today — see hook
  // comment) so pushes is the sensible default.
  defaultMetric?: AnalyticsMetric;
  defaultRange?: AnalyticsRange;
};

const RANGES: AnalyticsRange[] = ["24h", "7d", "30d"];

export function AnalyticsCard(props: AnalyticsCardProps): React.ReactElement {
  const {
    className,
    defaultMetric = "pushes",
    defaultRange = "7d",
  } = props;

  const [metric, setMetric] = React.useState<AnalyticsMetric>(defaultMetric);
  const [range, setRange] = React.useState<AnalyticsRange>(defaultRange);

  // Two hooks declared unconditionally; the non-matching one is disabled
  // so it never fires a network call. Cheaper than splitting the card
  // into two near-identical components.
  const tenantQ = useTenantAnalytics({
    metric,
    range,
    enabled: props.scope === "tenant",
  });
  const repoQ = useRepoAnalytics({
    org: props.scope === "repo" ? props.org : "",
    repo: props.scope === "repo" ? props.repo : "",
    metric,
    range,
    enabled: props.scope === "repo",
  });

  const q = props.scope === "tenant" ? tenantQ : repoQ;
  const data = q.data;

  return (
    <Card className={className}>
      <CardHeader className="pb-2">
        <div className="flex items-center justify-between gap-3">
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            {props.scope === "tenant"
              ? "Workspace activity"
              : "Repository activity"}
          </CardDescription>
          <MetricToggle value={metric} onChange={setMetric} />
        </div>
      </CardHeader>
      <CardContent className="pt-0">
        {q.isError ? (
          <ErrorState
            title="Couldn't load analytics"
            description="The audit service didn't answer. Retry, or check the BFF logs."
            onRetry={() => void q.refetch()}
          />
        ) : q.isLoading || !data ? (
          <SkeletonBody />
        ) : (
          <AnalyticsBody data={data} metric={metric} />
        )}

        <div className="mt-3 flex items-center justify-between">
          <RangeToggle value={range} onChange={setRange} />
          {data ? (
            <span className="text-[11px] text-[var(--color-fg-subtle)]">
              bucket {humanizeSeconds(data.bucket_size_secs)} · {data.buckets.length}{" "}
              points
            </span>
          ) : null}
        </div>
      </CardContent>
    </Card>
  );
}

function AnalyticsBody({
  data,
  metric,
}: {
  data: AnalyticsResponse;
  metric: AnalyticsMetric;
}): React.ReactElement {
  // Zeroed window — render the empty-state line rather than an SVG that
  // collapses into a single flat tick.
  const allZero = data.buckets.every((b) => b.count === 0);
  return (
    <div className="space-y-2">
      <div className="flex items-baseline justify-between gap-3">
        <div>
          <div className="font-display text-3xl font-medium leading-none tracking-tight">
            {formatCompactNumber(data.total)}
          </div>
          <div className="mt-1 text-xs text-[var(--color-fg-muted)]">
            total {metric} · last {data.range}
          </div>
        </div>
      </div>

      <Sparkline buckets={data.buckets} />

      {allZero ? (
        <p className="text-[11px] text-[var(--color-fg-subtle)]">
          No {metric} recorded in this window.
          {metric === "pulls"
            ? " Pull events are sampled (PULL_EVENT_SAMPLE_RATE) — a quiet window, or a low sample rate, can read as zero."
            : ""}
        </p>
      ) : null}
    </div>
  );
}

// Sparkline — an inline SVG line+area chart. 600×80 viewBox + responsive
// width via `preserveAspectRatio=none` so the card width drives the
// stretch. Y axis auto-scales to the max bucket count (with a 1 floor so
// flat-zero series still render the baseline).
function Sparkline({
  buckets,
}: {
  buckets: AnalyticsResponse["buckets"];
}): React.ReactElement {
  const W = 600;
  const H = 80;
  const P = 4; // padding so the stroke doesn't clip
  const n = buckets.length;
  const max = Math.max(1, ...buckets.map((b) => b.count));
  const stepX = n > 1 ? (W - 2 * P) / (n - 1) : 0;
  const yOf = (c: number) => H - P - ((H - 2 * P) * c) / max;

  const points = buckets.map((b, i) => {
    const x = P + i * stepX;
    const y = yOf(b.count);
    return [x, y] as const;
  });
  const linePath = points
    .map(([x, y], i) => `${i === 0 ? "M" : "L"}${x.toFixed(1)},${y.toFixed(1)}`)
    .join(" ");
  const areaPath = `${linePath} L${(P + (n - 1) * stepX).toFixed(1)},${H - P} L${P},${H - P} Z`;

  return (
    <svg
      viewBox={`0 0 ${W} ${H}`}
      preserveAspectRatio="none"
      className="block h-16 w-full"
      role="img"
      aria-label="Activity time-series"
    >
      <defs>
        <linearGradient id="spark-fill" x1="0" x2="0" y1="0" y2="1">
          <stop offset="0%" stopColor="var(--color-accent)" stopOpacity="0.28" />
          <stop offset="100%" stopColor="var(--color-accent)" stopOpacity="0" />
        </linearGradient>
      </defs>
      <path d={areaPath} fill="url(#spark-fill)" />
      <path
        d={linePath}
        fill="none"
        stroke="var(--color-accent)"
        strokeWidth={1.5}
        strokeLinecap="round"
        strokeLinejoin="round"
        vectorEffect="non-scaling-stroke"
      />
    </svg>
  );
}

function MetricToggle({
  value,
  onChange,
}: {
  value: AnalyticsMetric;
  onChange: (m: AnalyticsMetric) => void;
}): React.ReactElement {
  return (
    <div
      className="inline-flex rounded-md border border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] p-0.5"
      role="tablist"
      aria-label="Metric"
    >
      <ToggleChip
        active={value === "pushes"}
        onClick={() => onChange("pushes")}
        icon={<ArrowUpToLine className="size-3" />}
        label="Pushes"
      />
      <ToggleChip
        active={value === "pulls"}
        onClick={() => onChange("pulls")}
        icon={<ArrowDownToLine className="size-3" />}
        label="Pulls"
      />
    </div>
  );
}

function RangeToggle({
  value,
  onChange,
}: {
  value: AnalyticsRange;
  onChange: (r: AnalyticsRange) => void;
}): React.ReactElement {
  return (
    <div
      className="inline-flex rounded-md border border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] p-0.5"
      role="tablist"
      aria-label="Range"
    >
      {RANGES.map((r) => (
        <ToggleChip
          key={r}
          active={value === r}
          onClick={() => onChange(r)}
          label={r}
        />
      ))}
    </div>
  );
}

function ToggleChip({
  active,
  onClick,
  icon,
  label,
}: {
  active: boolean;
  onClick: () => void;
  icon?: React.ReactNode;
  label: string;
}): React.ReactElement {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      onClick={onClick}
      className={cn(
        "inline-flex items-center gap-1 rounded px-2 py-0.5 text-[11px] font-medium transition-colors",
        active
          ? "bg-[var(--color-surface)] text-[var(--color-fg)] shadow-sm"
          : "text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]",
      )}
    >
      {icon}
      {label}
    </button>
  );
}

function SkeletonBody(): React.ReactElement {
  return (
    <div className="space-y-2">
      <Skeleton className="h-9 w-32" />
      <Skeleton className="h-16 w-full rounded-md" />
    </div>
  );
}

function humanizeSeconds(s: number): string {
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.round(s / 60)}m`;
  if (s < 86_400) return `${Math.round(s / 3600)}h`;
  return `${Math.round(s / 86_400)}d`;
}


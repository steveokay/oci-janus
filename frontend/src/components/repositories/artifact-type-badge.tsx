import * as React from "react";
import { Box, Ship, FileSignature, FileCheck2, Package } from "lucide-react";
import { cn } from "@/lib/utils";
import type { ArtifactType } from "@/lib/api/types";

// Canonical per-artifact-type badge. image = cyan, helm = amber (the two
// first-class catalog types); signature/sbom/other get neutral treatments.
// Unlike the dense per-tag pill in tags-panel.tsx (which hides "image"),
// this badge labels every type — it's used where seeing "this is an image"
// at a glance matters (the repo Type column, the org cards).
//
// Token note: the theme has no dedicated cyan/`--color-info` token; the
// palette's cyan-family semantic is `--color-accent` (teal hue 178), so
// image uses it. helm uses `--color-warning` (amber). Both track dark-mode
// swaps automatically since they resolve through the semantic layer.
const CONFIG: Record<
  Exclude<ArtifactType, "">,
  { label: string; classes: string; Icon: React.ComponentType<{ className?: string }> }
> = {
  image: {
    label: "Image",
    classes:
      "border-[color:var(--color-accent)]/40 bg-[color:var(--color-accent)]/12 text-[color:var(--color-accent)]",
    Icon: Box,
  },
  helm: {
    label: "Helm chart",
    classes:
      "border-[color:var(--color-warning)]/40 bg-[color:var(--color-warning)]/12 text-[color:var(--color-warning)]",
    Icon: Ship,
  },
  signature: {
    label: "Signature",
    classes:
      "border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] text-[var(--color-fg-muted)]",
    Icon: FileSignature,
  },
  sbom: {
    label: "SBOM",
    classes:
      "border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] text-[var(--color-fg-muted)]",
    Icon: FileCheck2,
  },
  other: {
    label: "Artifact",
    classes:
      "border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] text-[var(--color-fg-muted)]",
    Icon: Package,
  },
};

// ArtifactTypeBadge renders a single labelled badge. Returns null for the
// empty artifact type.
export function ArtifactTypeBadge({ type }: { type: ArtifactType }): React.ReactElement | null {
  if (!type) return null;
  const c = CONFIG[type];
  const Icon = c.Icon;
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[10px] font-semibold",
        c.classes,
      )}
    >
      <Icon className="size-2.5" aria-hidden />
      {c.label}
    </span>
  );
}

// ArtifactTypeBadges renders one badge per type in a repo's artifact_types,
// ordered image → helm → others. Renders nothing when the repo has no
// manifests (empty/undefined list).
export function ArtifactTypeBadges({ types }: { types?: ArtifactType[] }): React.ReactElement | null {
  if (!types || types.length === 0) return null;
  const order: ArtifactType[] = ["image", "helm", "signature", "sbom", "other"];
  const sorted = [...types].sort((a, b) => order.indexOf(a) - order.indexOf(b));
  return (
    <span className="flex flex-wrap gap-1.5">
      {sorted.map((t) => (
        <ArtifactTypeBadge key={t} type={t} />
      ))}
    </span>
  );
}

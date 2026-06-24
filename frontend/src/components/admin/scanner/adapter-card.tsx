import * as React from "react";
import { CheckCircle2, Play, ShieldCheck } from "lucide-react";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type { AdminAdapter } from "@/lib/api/admin-scanners";
import { formatBytes } from "@/lib/format";

interface AdapterCardProps {
  adapter: AdminAdapter;
  // Disable card-level actions while a swap is in-flight elsewhere on the
  // page (one PATCH at a time keeps the cache coherent).
  busy?: boolean;
  // Disable the test-scan button while a test scan is running.
  testing?: boolean;
  // Name of the currently-active adapter, used to build the replace-action
  // copy on non-active cards ("Replace <name> with this"). Undefined when
  // no adapter is currently active — then the button falls back to
  // "Make active".
  currentActiveName?: string;
  onMakeActive: () => void;
  onRunTestScan: () => void;
}

// Beacon — AdapterCard.
//
// One card per installed scanner-adapter binary. Active adapter gets:
//   - `accentBar="success"` along the top edge
//   - an "Active" Badge in the header
//   - a primary "Run test scan" button in the footer
// Non-active adapters get a ghost "Replace <activeName> with this" button so
// the operator sees what they'd be displacing without having to scan the
// grid for the green pill. Checksum is truncated to 16 chars with the full
// digest in `title` (browser native tooltip — Beacon doesn't yet have a
// Tooltip primitive and the brief forbids new primitives).
export function AdapterCard({
  adapter,
  busy,
  testing,
  currentActiveName,
  onMakeActive,
  onRunTestScan,
}: AdapterCardProps): React.ReactElement {
  const shortChecksum = adapter.checksum.slice(0, 16);
  return (
    <Card
      accentBar={adapter.active ? "success" : "neutral"}
      className="flex flex-col"
    >
      <CardHeader className="pb-3">
        <div className="flex items-start justify-between gap-2">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <ShieldCheck
                className="size-4 shrink-0 text-[var(--color-fg-muted)]"
                aria-hidden
              />
              <h3 className="truncate font-display text-base font-medium tracking-tight text-[var(--color-fg)]">
                {adapter.name}
              </h3>
            </div>
            <div className="mt-1 font-mono text-xs text-[var(--color-fg-muted)]">
              v{adapter.version}
            </div>
          </div>
          {adapter.active ? (
            <Badge tone="success" className="shrink-0">
              <CheckCircle2 className="size-3" />
              Active
            </Badge>
          ) : null}
        </div>
      </CardHeader>

      <CardContent className="flex-1 space-y-3 pt-0 pb-4">
        <Field label="Path">
          <code className="block break-all font-mono text-xs text-[var(--color-fg-muted)]">
            {adapter.path}
          </code>
        </Field>

        <div className="grid grid-cols-2 gap-3">
          <Field label="Size">
            <span className="font-mono text-xs tabular-nums text-[var(--color-fg)]">
              {formatBytes(adapter.size_bytes)}
            </span>
          </Field>
          <Field label="Checksum">
            <code
              className="font-mono text-xs text-[var(--color-fg)]"
              title={`sha256:${adapter.checksum}`}
            >
              {shortChecksum}…
            </code>
          </Field>
        </div>

        {adapter.env_keys.length > 0 ? (
          <Field label="Env vars">
            <div className="flex flex-wrap gap-1">
              {adapter.env_keys.map((k) => (
                <span
                  key={k}
                  className="rounded-full border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-2 py-0.5 font-mono text-[10px] text-[var(--color-fg-muted)]"
                >
                  {k}
                </span>
              ))}
            </div>
          </Field>
        ) : (
          <Field label="Env vars">
            <span className="text-xs text-[var(--color-fg-subtle)]">
              None read at scan time.
            </span>
          </Field>
        )}
      </CardContent>

      {/* Footer action — primary on the active card (test scan), ghost
          elsewhere (promote). Kept inside CardContent vs CardFooter so the
          card can flex to a consistent height across the grid. */}
      <div className="border-t border-[var(--color-border)] p-4">
        {adapter.active ? (
          <Button
            variant="accent"
            size="sm"
            className="w-full"
            onClick={onRunTestScan}
            disabled={testing || busy}
            loading={testing}
          >
            <Play className="size-3.5" />
            {testing ? "Running test scan" : "Run test scan"}
          </Button>
        ) : (
          <Button
            variant="ghost"
            size="sm"
            className="w-full"
            onClick={onMakeActive}
            disabled={busy}
          >
            {currentActiveName ? (
              <>
                Replace{" "}
                <span className="font-medium text-[var(--color-fg)]">
                  {currentActiveName}
                </span>{" "}
                with this
              </>
            ) : (
              "Make active"
            )}
          </Button>
        )}
      </div>
    </Card>
  );
}

function Field({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}): React.ReactElement {
  return (
    <div>
      <div className="text-[10px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
        {label}
      </div>
      <div className="mt-1">{children}</div>
    </div>
  );
}

import * as React from "react";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { MoreHorizontal, ShieldCheck, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  useOIDCTrusts,
  useDeleteOIDCTrust,
  type OIDCTrust,
} from "@/lib/api/oidc-trust";
import { CreateOIDCTrustDialog } from "./CreateOIDCTrustDialog";
import { formatRelativeDate, formatAbsoluteDate } from "@/lib/format";

// TrustPanel — live FUT-001 federated-trust surface. Replaces
// TrustPreview. Mirrors the shape of HelpersPanel (unconditional header,
// loading / error / empty states below it).
//
// Kept from the preview:
//   - The "How it works" flow diagram (SVG + text fallback). Still
//     accurate as an operator mental model, so we inline it verbatim.
//
// Removed vs. the preview:
//   - <PreviewBanner> — surface is live, no draft-state kicker.
//   - Aria-disabled placeholder button — replaced with a real
//     "New trust relationship" button that opens the create dialog.
//
// Edit is deferred to a REM follow-up; the kebab menu only exposes
// Delete today.

// formatLastVerified returns a human-readable "last verified" string.
// null / undefined maps to a sentinel — copy matches the preview so
// existing docs still make sense. Now uses the shared formatRelativeDate
// ("3 days ago") for app-wide timestamp consistency; the absolute form is
// surfaced via a title tooltip at the call site.
function formatLastVerified(iso: string | null | undefined): string {
  if (!iso) return "never";
  return formatRelativeDate(iso);
}

export function TrustPanel(): React.ReactElement {
  const trusts = useOIDCTrusts();
  const deleteTrust = useDeleteOIDCTrust();
  const [createOpen, setCreateOpen] = React.useState(false);

  // handleDelete — wraps the mutation. Called from the kebab menu.
  function handleDelete(id: string): void {
    deleteTrust.mutate(id);
  }

  return (
    <div className="space-y-6">
      {/* Header — unconditional; matches HelpersPanel. */}
      <header className="flex flex-col gap-1">
        <h1 className="font-display text-3xl font-medium tracking-tight">
          Federated trust
        </h1>
        <p className="text-sm text-[var(--color-fg-muted)]">
          Allow CI runners to authenticate with a short-lived registry token
          via OIDC — no static API key required.
        </p>
      </header>

      {trusts.isLoading ? (
        <div role="status" className="text-sm text-[var(--color-fg-muted)]">
          Loading federated trusts&hellip;
        </div>
      ) : trusts.isError ? (
        <div role="alert" className="text-sm text-[var(--color-danger)]">
          Failed to load federated trusts. Try refreshing the page.
        </div>
      ) : (
        <>
          {/* New-trust button — opens the create dialog. */}
          <div>
            <Button
              variant="accent"
              onClick={() => setCreateOpen(true)}
            >
              New trust relationship
            </Button>
          </div>

          {/* Trust cards or empty state. */}
          {trusts.data && trusts.data.length > 0 ? (
            <ul
              className="space-y-3"
              aria-label="Configured trust relationships"
            >
              {trusts.data.map((trust) => (
                <TrustCard
                  key={trust.id}
                  trust={trust}
                  onDelete={handleDelete}
                />
              ))}
            </ul>
          ) : (
            <p className="rounded-lg border border-dashed border-[var(--color-border)] bg-[var(--color-bg-subtle)] px-6 py-8 text-center text-sm text-[var(--color-fg-muted)]">
              No federated trusts yet. Create one to let a CI provider
              authenticate without static API keys.
            </p>
          )}

          {/* Inline flow diagram — text + SVG. Verbatim from TrustPreview
              because the operator-facing explanation didn't change when
              the surface went live. */}
          <section aria-label="Authentication flow diagram">
            <h2 className="mb-3 text-sm font-medium text-[var(--color-fg-muted)]">
              How it works
            </h2>
            <div
              className="overflow-x-auto rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-surface)] p-6"
              aria-label="Authentication flow: GHA runner produces an OIDC JWT, which registry-auth validates and exchanges for a short-lived registry token, which the runner uses to push or pull from registry-core."
            >
              <svg
                viewBox="0 0 520 64"
                aria-hidden="true"
                className="w-full max-w-xl"
              >
                <defs>
                  <marker
                    id="arrowhead"
                    markerWidth="7"
                    markerHeight="7"
                    refX="6"
                    refY="3.5"
                    orient="auto"
                  >
                    <polygon
                      points="0 0, 7 3.5, 0 7"
                      className="fill-current text-[var(--color-fg-muted)]"
                    />
                  </marker>
                </defs>
                <rect
                  x="0"
                  y="16"
                  width="110"
                  height="32"
                  rx="6"
                  className="fill-amber-50 stroke-amber-300 dark:fill-amber-950/40 dark:stroke-amber-700"
                  strokeWidth="1"
                />
                <text
                  x="55"
                  y="36"
                  textAnchor="middle"
                  className="fill-current text-xs"
                  style={{ fontSize: 11 }}
                >
                  GHA runner
                </text>

                <rect
                  x="155"
                  y="16"
                  width="110"
                  height="32"
                  rx="6"
                  className="fill-amber-50 stroke-amber-300 dark:fill-amber-950/40 dark:stroke-amber-700"
                  strokeWidth="1"
                />
                <text
                  x="210"
                  y="36"
                  textAnchor="middle"
                  className="fill-current text-xs"
                  style={{ fontSize: 11 }}
                >
                  registry-auth
                </text>

                <rect
                  x="310"
                  y="16"
                  width="110"
                  height="32"
                  rx="6"
                  className="fill-amber-50 stroke-amber-300 dark:fill-amber-950/40 dark:stroke-amber-700"
                  strokeWidth="1"
                />
                <text
                  x="365"
                  y="36"
                  textAnchor="middle"
                  className="fill-current text-xs"
                  style={{ fontSize: 11 }}
                >
                  registry-core
                </text>

                <line
                  x1="110"
                  y1="32"
                  x2="153"
                  y2="32"
                  stroke="currentColor"
                  strokeWidth="1.5"
                  markerEnd="url(#arrowhead)"
                  className="text-[var(--color-fg-muted)]"
                />
                <text
                  x="132"
                  y="26"
                  textAnchor="middle"
                  style={{ fontSize: 9 }}
                  className="fill-current text-[var(--color-fg-subtle)]"
                >
                  OIDC JWT
                </text>

                <line
                  x1="265"
                  y1="32"
                  x2="308"
                  y2="32"
                  stroke="currentColor"
                  strokeWidth="1.5"
                  markerEnd="url(#arrowhead)"
                  className="text-[var(--color-fg-muted)]"
                />
                <text
                  x="287"
                  y="26"
                  textAnchor="middle"
                  style={{ fontSize: 9 }}
                  className="fill-current text-[var(--color-fg-subtle)]"
                >
                  short-lived token
                </text>
              </svg>

              <p className="mt-3 font-mono text-xs text-[var(--color-fg-muted)]">
                GHA runner → OIDC JWT → registry-auth → short-lived registry
                token → registry-core
              </p>
            </div>
          </section>
        </>
      )}

      {/* Create dialog — always mounted so the state resets cleanly. */}
      <CreateOIDCTrustDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
      />
    </div>
  );
}

// TrustCard — one trust config with a kebab menu. Extracted so the
// DropdownMenu instances don't collide (each has its own trigger).
function TrustCard({
  trust,
  onDelete,
}: {
  trust: OIDCTrust;
  onDelete: (id: string) => void;
}): React.ReactElement {
  return (
    <li className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-surface)] p-6">
      <div className="flex items-start justify-between gap-4">
        <div className="flex items-center gap-3">
          <div
            aria-hidden="true"
            className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-[var(--color-bg-subtle)]"
          >
            <ShieldCheck className="size-4 text-[var(--color-fg-muted)]" />
          </div>
          <div>
            <p className="text-sm font-medium">{trust.display_name}</p>
            <p className="font-mono text-xs text-[var(--color-fg-muted)]">
              {trust.subject_pattern}
            </p>
            <p className="mt-1 text-[11px] text-[var(--color-fg-subtle)]">
              {trust.issuer_url} — audience: {trust.audience}
            </p>
          </div>
        </div>
        <div className="flex items-center gap-3">
          <span
            className="shrink-0 text-xs text-[var(--color-fg-subtle)]"
            // Absolute timestamp on hover; only meaningful when there IS a
            // last-used timestamp (otherwise the relative form is "never").
            title={
              trust.last_used_at
                ? formatAbsoluteDate(trust.last_used_at)
                : undefined
            }
          >
            Last verified: {formatLastVerified(trust.last_used_at)}
          </span>
          <DropdownMenu.Root>
            <DropdownMenu.Trigger asChild>
              <Button
                variant="ghost"
                size="sm"
                aria-label={`Trust actions for ${trust.display_name}`}
              >
                <MoreHorizontal className="size-4" aria-hidden />
              </Button>
            </DropdownMenu.Trigger>
            <DropdownMenu.Portal>
              <DropdownMenu.Content
                align="end"
                className="z-50 min-w-[160px] rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] p-1 shadow-[var(--shadow-card)]"
              >
                <DropdownMenu.Item
                  onSelect={() => onDelete(trust.id)}
                  className="flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-sm text-[var(--color-danger)] outline-none hover:bg-[var(--color-surface-sunken)]"
                >
                  <Trash2 className="size-3.5" aria-hidden />
                  Delete
                </DropdownMenu.Item>
              </DropdownMenu.Content>
            </DropdownMenu.Portal>
          </DropdownMenu.Root>
        </div>
      </div>
    </li>
  );
}

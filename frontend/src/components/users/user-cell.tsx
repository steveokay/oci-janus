import * as React from "react";
import { Shield } from "lucide-react";
import { CopyButton } from "@/components/ui/copy-button";

// REM-018 Phase B — UserCell is the canonical primitive for rendering a
// principal across the dashboard. Replaces the raw-UUID renders that used
// to live inline in members-tables, activity rows, audit-event actor cells,
// and tenant "created by" columns.
//
// Rendering rules (locked):
//   - `display_name` and `username` both empty → "System" placeholder with a
//     shield icon. This is the natural shape from the backend when a row's
//     granted_by is the zero UUID (no LEFT JOIN match in services/auth).
//   - `display_name` equals `username` (the auth service's COALESCE chain
//     falls through to username when the user hasn't set a display_name) →
//     render only `@username`. Avoids visual stutter ("alice (@alice)").
//   - `display_name` distinct from `username` → primary `display_name` plus
//     a subdued `@username` line. Mirrors the GitHub / Slack / Linear shape
//     operators are used to.
//   - The full `user_id` UUID is always available in a tooltip on the
//     primary text + has a copy button when the `withCopy` prop is set
//     (members-tables use this; the activity feed does not).

export type UserCellVariant = "default" | "inline";

export interface UserCellProps {
  userId: string;
  username: string;
  displayName: string;
  /**
   * "default" renders the avatar + two-line label (members-table shape).
   * "inline" drops the avatar + flattens to a single text line for activity
   * feeds, audit rows, and other dense surfaces.
   */
  variant?: UserCellVariant;
  /**
   * When true, a per-cell CopyButton on the user_id UUID is added so admins
   * can paste it into a curl / SQL context without expanding the tooltip.
   * Off by default — the tooltip alone covers the casual lookup.
   */
  withCopy?: boolean;
}

export function UserCell({
  userId,
  username,
  displayName,
  variant = "default",
  withCopy = false,
}: UserCellProps): React.ReactElement {
  const isSystem = !username && !displayName;
  // Distinct label only when display_name is set AND differs from the
  // username fallback. Avoids the "alice (@alice)" double-render.
  const hasDistinctLabel =
    displayName !== "" && displayName !== username;
  const primary = isSystem
    ? "System"
    : hasDistinctLabel
      ? displayName
      : `@${username}`;
  // The "@<username>" suffix only renders when there IS a distinct primary.
  const secondary = hasDistinctLabel ? `@${username}` : "";

  if (variant === "inline") {
    return (
      <span className="inline-flex items-center gap-1.5 text-sm">
        {isSystem ? (
          <Shield
            className="size-3.5 text-[var(--color-fg-subtle)]"
            aria-hidden
          />
        ) : null}
        <PrimaryWithTooltip
          text={primary}
          userId={userId}
          className="font-medium text-[var(--color-fg)]"
        />
        {secondary ? (
          <span className="text-[var(--color-fg-muted)]">{secondary}</span>
        ) : null}
      </span>
    );
  }

  return (
    <div className="flex items-center gap-3">
      <Avatar isSystem={isSystem} seed={displayName || username || userId} />
      <div className="min-w-0">
        <PrimaryWithTooltip
          text={primary}
          userId={userId}
          className="block truncate text-sm font-medium text-[var(--color-fg)]"
        />
        {secondary ? (
          <div className="truncate text-xs text-[var(--color-fg-muted)]">
            {secondary}
          </div>
        ) : isSystem ? (
          <div className="text-[10px] uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            system actor
          </div>
        ) : null}
      </div>
      {withCopy && !isSystem ? (
        <CopyButton value={userId} iconOnly />
      ) : null}
    </div>
  );
}

// Avatar — initial from the label seed (display name first, username
// fallback, then UUID). System actors get a shield icon instead so the
// row is visually distinct from a human/SA user.
function Avatar({
  isSystem,
  seed,
}: {
  isSystem: boolean;
  seed: string;
}): React.ReactElement {
  if (isSystem) {
    return (
      <span
        className="grid size-8 shrink-0 place-items-center rounded-md bg-[var(--color-fg-subtle)]/15 text-[var(--color-fg-subtle)]"
        aria-hidden
      >
        <Shield className="size-4" />
      </span>
    );
  }
  // Same deterministic single-char shape as the old members-table avatar.
  // Strip leading "@" so a username-only label still picks the first
  // letter of the actual handle, not the @.
  const cleaned = seed.replace(/^@/, "").replace(/[^a-z0-9]/gi, "");
  const ch = (cleaned[0] ?? "·").toUpperCase();
  return (
    <span
      className="grid size-8 shrink-0 place-items-center rounded-md bg-[var(--color-accent-subtle)] font-display text-sm font-semibold text-[var(--color-accent)]"
      aria-hidden
    >
      {ch}
    </span>
  );
}

// PrimaryWithTooltip — wraps the primary label so the underlying UUID
// stays one hover away. Operators routinely need the UUID for curl + SQL
// flows, so it must remain discoverable without polluting the visible row.
// Uses the native `title` attribute (no custom Tooltip primitive lives in
// frontend/components/ui yet, and the native shape is accessible by default
// — screen readers announce it and there's no JS-portal weirdness).
function PrimaryWithTooltip({
  text,
  userId,
  className,
}: {
  text: string;
  userId: string;
  className?: string;
}): React.ReactElement {
  return (
    <span className={className} title={userId || undefined}>
      {text}
    </span>
  );
}

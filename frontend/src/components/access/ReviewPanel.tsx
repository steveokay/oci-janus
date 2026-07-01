import * as React from "react";
import { AlertTriangle } from "lucide-react";
import { toast } from "sonner";
import { AxiosError } from "axios";
import {
  useStaleKeys,
  useSnoozeKey,
  type StaleKey,
  type SuggestedAction,
} from "@/lib/api/access-review";
import { useDeleteApiKey } from "@/lib/api/api-keys";
import { formatRelativeDate } from "@/lib/format";

// ReviewPanel — live FUT-004 access-review surface. Replaces
// ReviewPreview. Mirrors the shape of PoliciesPanel / TrustPanel
// (unconditional header, loading / error states below it, populated
// state below that).
//
// Semantics vs. the preview:
//   - Table is fed by GET /access/review/stale on the BFF.
//   - Each row exposes three actions:
//       Revoke     — reuses the existing DELETE /apikeys/:id primitive.
//                    Emphasised when suggested_action === "REVOKE".
//       Keep       — no backend call in v1. The row is dropped from
//                    the local rendered list; the next weekly worker
//                    tick will re-surface it if still stale.
//       Snooze 30d — POST /access/review/snooze { key_id, days: 30 }.
//   - Toast on success mirrors the tenant-users dialogs' pattern
//     (sonner, distinct copy per action).
//
// Reason string is user-facing but comes from a fixed BE enum
// ("idle" | "rotation_lapsed" | "both" | ""); reasonCopy maps it to
// human copy so the table cell stays legible.

// reasonCopy — human-readable label for the wire `reason` value.
const REASON_COPY: Record<StaleKey["reason"], string> = {
  idle: "Not used recently",
  rotation_lapsed: "Rotation overdue",
  both: "Idle and rotation overdue",
  "": "Flagged for review",
};

// ReviewPanel — top-level live surface.
export function ReviewPanel(): React.ReactElement {
  const stale = useStaleKeys();
  const snooze = useSnoozeKey();
  const revoke = useDeleteApiKey();

  // Local "kept" set — key IDs the operator has clicked Keep on this
  // session. Filtered out of the render but not persisted; the weekly
  // worker will re-surface them next tick if still stale, matching the
  // plan's "no backend call in v1" contract.
  const [keptIds, setKeptIds] = React.useState<Set<string>>(
    () => new Set(),
  );

  // handleRevoke — soft-delete a key via the existing personal-key
  // primitive. Toasts + relies on invalidation to drop the row from
  // the list on refresh; also nudges it into keptIds so the row
  // disappears optimistically without waiting on the next fetch.
  function handleRevoke(key: StaleKey): void {
    revoke.mutate(key.id, {
      onSuccess: () => {
        toast.success(`Revoked ${key.name}.`);
        setKeptIds((prev) => {
          const next = new Set(prev);
          next.add(key.id);
          return next;
        });
      },
      onError: (err) => {
        const status = (err as AxiosError | undefined)?.response?.status;
        toast.error(
          status === 403
            ? "You don't have permission to revoke this key."
            : status === 404
              ? "Key not found — it may have already been revoked."
              : "Revoke failed. Try again or check the BFF logs.",
        );
      },
    });
  }

  // handleKeep — remove the row from the local rendered list. Cheap
  // UX affordance; the weekly worker owns the source of truth so if
  // it's still stale next tick, it'll come back.
  function handleKeep(key: StaleKey): void {
    setKeptIds((prev) => {
      const next = new Set(prev);
      next.add(key.id);
      return next;
    });
    toast.success(`Kept ${key.name}.`);
  }

  // handleSnooze — POST snooze with 30 days. On success the query
  // invalidation drops the row; toast confirms the action.
  function handleSnooze(key: StaleKey): void {
    snooze.mutate(
      { key_id: key.id, days: 30 },
      {
        onSuccess: () => {
          toast.success(`Snoozed ${key.name} for 30 days.`);
        },
        onError: (err) => {
          const status = (err as AxiosError | undefined)?.response
            ?.status;
          toast.error(
            status === 403
              ? "You don't have permission to snooze this key."
              : "Snooze failed. Try again or check the BFF logs.",
          );
        },
      },
    );
  }

  // Filter locally-kept rows out of the render. Uses the server list
  // as the source of truth and treats keptIds as a session-only mask.
  const visibleKeys: StaleKey[] = React.useMemo(() => {
    if (!stale.data) return [];
    return stale.data.filter((k) => !keptIds.has(k.id));
  }, [stale.data, keptIds]);

  return (
    <div className="space-y-6">
      {/* Header — unconditional; matches PoliciesPanel / TrustPanel. */}
      <header className="flex flex-col gap-1">
        <h1 className="font-display text-3xl font-medium tracking-tight">
          Access review
        </h1>
        <p className="text-sm text-[var(--color-fg-muted)]">
          Periodically review long-lived API keys that haven&apos;t
          been used recently and revoke credentials that are no longer
          needed.
        </p>
      </header>

      {stale.isLoading ? (
        <div
          role="status"
          className="text-sm text-[var(--color-fg-muted)]"
        >
          Loading access review&hellip;
        </div>
      ) : stale.isError ? (
        <div
          role="alert"
          className="text-sm text-[var(--color-danger)]"
        >
          Failed to load access review. Try refreshing the page.
        </div>
      ) : visibleKeys.length === 0 ? (
        <div
          role="status"
          className="text-sm text-[var(--color-fg-muted)]"
        >
          Nothing to review today — all keys are fresh.
        </div>
      ) : (
        <>
          {/* Amber alert banner — surfaced only when there are stale
              keys to review. Copy mirrors the preview's for continuity
              with the operator's mental model. */}
          <div
            role="alert"
            className="flex items-start gap-3 rounded-lg border border-amber-300 bg-amber-50 px-4 py-3 text-sm dark:border-amber-700 dark:bg-amber-950/40"
          >
            <AlertTriangle
              className="mt-0.5 size-4 shrink-0 text-amber-600 dark:text-amber-400"
              aria-hidden
            />
            <span>
              <strong className="font-medium">
                {visibleKeys.length}{" "}
                {visibleKeys.length === 1 ? "key" : "keys"}
              </strong>{" "}
              due for review.
            </span>
          </div>

          {/* Stale-key table. */}
          <div className="overflow-x-auto rounded-lg border border-[var(--color-border)]">
            <table className="w-full text-sm">
              <caption className="sr-only">
                Stale API keys pending review
              </caption>
              <thead>
                <tr className="border-b border-[var(--color-border)] bg-[var(--color-bg-subtle)]">
                  <th
                    scope="col"
                    className="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-[var(--color-fg-muted)]"
                  >
                    Key name
                  </th>
                  <th
                    scope="col"
                    className="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-[var(--color-fg-muted)]"
                  >
                    Owner
                  </th>
                  <th
                    scope="col"
                    className="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-[var(--color-fg-muted)]"
                  >
                    Last used
                  </th>
                  <th
                    scope="col"
                    className="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-[var(--color-fg-muted)]"
                  >
                    Reason
                  </th>
                  <th
                    scope="col"
                    className="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-[var(--color-fg-muted)]"
                  >
                    Actions
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-[var(--color-border)] bg-[var(--color-bg-surface)]">
                {visibleKeys.map((key) => (
                  <ReviewRow
                    key={key.id}
                    row={key}
                    onRevoke={() => handleRevoke(key)}
                    onKeep={() => handleKeep(key)}
                    onSnooze={() => handleSnooze(key)}
                    pending={
                      snooze.isPending || revoke.isPending
                    }
                  />
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
    </div>
  );
}

// ReviewRow — one row of the stale-key table. Extracted so the render
// pass stays readable and the per-row state (last_used_at formatting,
// suggested-action emphasis) is co-located with its buttons.
function ReviewRow({
  row,
  onRevoke,
  onKeep,
  onSnooze,
  pending,
}: {
  row: StaleKey;
  onRevoke: () => void;
  onKeep: () => void;
  onSnooze: () => void;
  pending: boolean;
}): React.ReactElement {
  const lastUsed = row.last_used_at
    ? formatRelativeDate(row.last_used_at)
    : "Never used";
  const reasonLabel = REASON_COPY[row.reason] ?? "Flagged for review";

  return (
    <tr>
      <td className="px-4 py-3 font-mono text-xs">{row.name}</td>
      <td className="px-4 py-3 text-[var(--color-fg-muted)]">
        {row.owner_user_id}
      </td>
      <td className="px-4 py-3 text-[var(--color-fg-muted)]">
        {lastUsed}
      </td>
      <td className="px-4 py-3 text-[var(--color-fg-muted)]">
        {reasonLabel}
      </td>
      <td className="px-4 py-3">
        <div
          className="flex items-center gap-2"
          role="group"
          aria-label={`Actions for ${row.name}`}
        >
          <ActionButton
            label="Revoke"
            onClick={onRevoke}
            emphasised={row.suggested_action === "REVOKE"}
            emphasisClass="bg-red-50 text-red-700 ring-1 ring-red-200 hover:bg-red-100 dark:bg-red-950/30 dark:text-red-400 dark:ring-red-800 dark:hover:bg-red-950/50"
            disabled={pending}
            testAttr="revoke"
          />
          <ActionButton
            label="Keep"
            onClick={onKeep}
            emphasised={row.suggested_action === "KEEP"}
            emphasisClass="bg-green-50 text-green-700 ring-1 ring-green-200 hover:bg-green-100 dark:bg-green-950/30 dark:text-green-400 dark:ring-green-800 dark:hover:bg-green-950/50"
            disabled={pending}
            testAttr="keep"
          />
          <ActionButton
            label="Snooze 30d"
            onClick={onSnooze}
            emphasised={row.suggested_action === "SNOOZE"}
            emphasisClass="bg-blue-50 text-blue-700 ring-1 ring-blue-200 hover:bg-blue-100 dark:bg-blue-950/30 dark:text-blue-400 dark:ring-blue-800 dark:hover:bg-blue-950/50"
            disabled={pending}
            testAttr="snooze"
          />
        </div>
      </td>
    </tr>
  );
}

// ActionButton — one of the three per-row action buttons. Encapsulates
// the "emphasised = suggested action" styling so the row markup stays
// symmetric. The `emphasisClass` is passed in so each button gets a
// distinct hue (red for revoke, green for keep, blue for snooze).
function ActionButton({
  label,
  onClick,
  emphasised,
  emphasisClass,
  disabled,
  testAttr,
}: {
  label: string;
  onClick: () => void;
  emphasised: boolean;
  emphasisClass: string;
  disabled: boolean;
  testAttr: string;
}): React.ReactElement {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      data-action={testAttr}
      data-suggested={emphasised ? "true" : undefined}
      className={[
        "rounded-md px-2.5 py-1 text-xs font-medium transition-colors disabled:opacity-60 disabled:cursor-not-allowed",
        emphasised
          ? emphasisClass
          : "border border-[var(--color-border)] text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-sunken)]",
      ].join(" ")}
    >
      {label}
    </button>
  );
}

// Re-export SuggestedAction so consumers/tests don't have to import
// it from the API module separately.
export type { SuggestedAction };

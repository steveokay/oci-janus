import * as React from "react";
import { AlertTriangle } from "lucide-react";
import { PreviewBanner } from "@/components/access/PreviewBanner";

// Dummy stale-key data — illustrative only (FUT-004 preview).
interface StaleKey {
  id: string;
  name: string;
  owner: string;
  lastUsed: string;
  suggestedAction: "Revoke" | "Keep" | "Snooze 30d";
}

const DUMMY_STALE_KEYS: StaleKey[] = [
  {
    id: "k1",
    name: "deploy-prod",
    owner: "alice@example.com",
    lastUsed: "45 days ago",
    suggestedAction: "Revoke",
  },
  {
    id: "k2",
    name: "ci-legacy",
    owner: "bob@example.com",
    lastUsed: "62 days ago",
    suggestedAction: "Revoke",
  },
  {
    id: "k3",
    name: "terraform-dev",
    owner: "carol@example.com",
    lastUsed: "33 days ago",
    suggestedAction: "Keep",
  },
  {
    id: "k4",
    name: "integration-test",
    owner: "dave@example.com",
    lastUsed: "38 days ago",
    suggestedAction: "Snooze 30d",
  },
  {
    id: "k5",
    name: "backup-sync",
    owner: "eve@example.com",
    lastUsed: "91 days ago",
    suggestedAction: "Revoke",
  },
];

// ReviewPreview — illustrative preview of the periodic access-review surface
// (FUT-004, shipping Sprint 12). All action buttons are disabled and carry
// aria-disabled + aria-describedby so screen readers can explain why.
export function ReviewPreview(): React.ReactElement {
  return (
    <div className="space-y-6">
      {/* Page header. */}
      <header className="flex flex-col gap-1">
        <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          Preview
        </p>
        <h1 className="font-display text-3xl font-medium tracking-tight">
          Access review
        </h1>
        <p className="text-sm text-[var(--color-fg-muted)]">
          Periodically review long-lived API keys that haven't been used
          recently and revoke credentials that are no longer needed.
        </p>
      </header>

      {/* Amber preview notice. */}
      <PreviewBanner sprint="Sprint 12" futureID="FUT-004" />

      {/* Hidden reason text — referenced by all disabled controls below. */}
      <p id="review-disabled-reason" className="sr-only">
        Available in Sprint 12 (FUT-004). These controls are not yet
        functional.
      </p>

      {/* Stale-key alert banner. */}
      <div
        role="alert"
        className="flex items-start gap-3 rounded-lg border border-amber-300 bg-amber-50 px-4 py-3 text-sm dark:border-amber-700 dark:bg-amber-950/40"
      >
        <AlertTriangle
          className="mt-0.5 size-4 shrink-0 text-amber-600 dark:text-amber-400"
          aria-hidden
        />
        <span>
          <strong className="font-medium">5 keys</strong> haven't been used in
          30 days. Review and revoke?
        </span>
      </div>

      {/* Stale-key table. */}
      <div className="overflow-x-auto rounded-lg border border-[var(--color-border)]">
        <table className="w-full text-sm">
          <caption className="sr-only">Stale API keys pending review</caption>
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
                Last used
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
                Suggested action
              </th>
            </tr>
          </thead>
          <tbody className="divide-y divide-[var(--color-border)] bg-[var(--color-bg-surface)]">
            {DUMMY_STALE_KEYS.map((key) => (
              <tr key={key.id}>
                <td className="px-4 py-3 font-mono text-xs">{key.name}</td>
                <td className="px-4 py-3 text-[var(--color-fg-muted)]">
                  {key.lastUsed}
                </td>
                <td className="px-4 py-3 text-[var(--color-fg-muted)]">
                  {key.owner}
                </td>
                <td className="px-4 py-3">
                  {/* Action buttons — all three disabled per the a11y spec. */}
                  <div className="flex items-center gap-2" role="group" aria-label={`Actions for ${key.name}`}>
                    <button
                      type="button"
                      disabled
                      aria-disabled="true"
                      aria-describedby="review-disabled-reason"
                      data-suggested={
                        key.suggestedAction === "Revoke" ? "true" : undefined
                      }
                      className={[
                        "rounded-md px-2.5 py-1 text-xs font-medium opacity-60 cursor-not-allowed",
                        key.suggestedAction === "Revoke"
                          ? "bg-red-50 text-red-700 ring-1 ring-red-200 dark:bg-red-950/30 dark:text-red-400 dark:ring-red-800"
                          : "border border-[var(--color-border)] text-[var(--color-fg-muted)]",
                      ].join(" ")}
                    >
                      Revoke
                    </button>
                    <button
                      type="button"
                      disabled
                      aria-disabled="true"
                      aria-describedby="review-disabled-reason"
                      className={[
                        "rounded-md px-2.5 py-1 text-xs font-medium opacity-60 cursor-not-allowed",
                        key.suggestedAction === "Keep"
                          ? "bg-green-50 text-green-700 ring-1 ring-green-200 dark:bg-green-950/30 dark:text-green-400 dark:ring-green-800"
                          : "border border-[var(--color-border)] text-[var(--color-fg-muted)]",
                      ].join(" ")}
                    >
                      Keep
                    </button>
                    <button
                      type="button"
                      disabled
                      aria-disabled="true"
                      aria-describedby="review-disabled-reason"
                      className={[
                        "rounded-md px-2.5 py-1 text-xs font-medium opacity-60 cursor-not-allowed",
                        key.suggestedAction === "Snooze 30d"
                          ? "bg-blue-50 text-blue-700 ring-1 ring-blue-200 dark:bg-blue-950/30 dark:text-blue-400 dark:ring-blue-800"
                          : "border border-[var(--color-border)] text-[var(--color-fg-muted)]",
                      ].join(" ")}
                    >
                      Snooze 30d
                    </button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {/* Send reminders button — disabled. */}
      <button
        type="button"
        disabled
        aria-disabled="true"
        aria-describedby="review-disabled-reason"
        className="inline-flex items-center rounded-md border border-[var(--color-border)] bg-[var(--color-bg-surface)] px-4 py-2 text-sm font-medium text-[var(--color-fg-muted)] opacity-60 cursor-not-allowed"
      >
        Send review reminders to owners
      </button>
    </div>
  );
}

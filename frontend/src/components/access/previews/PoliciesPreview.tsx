import * as React from "react";
import { PreviewBanner } from "@/components/access/PreviewBanner";

// PoliciesPreview — illustrative preview of the token-policy surface
// (FUT-003, shipping Sprint 12). All controls are disabled and carry
// aria-disabled + aria-describedby pointing to the reason text.
export function PoliciesPreview(): React.ReactElement {
  return (
    <div className="space-y-6">
      {/* Page header. */}
      <header className="flex flex-col gap-1">
        <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          Preview
        </p>
        <h1 className="font-display text-3xl font-medium tracking-tight">
          Token policies
        </h1>
        <p className="text-sm text-[var(--color-fg-muted)]">
          Enforce workspace-wide limits on token lifetime and rotation cadence.
        </p>
      </header>

      {/* Amber preview notice. */}
      <PreviewBanner sprint="Sprint 12" futureID="FUT-003" />

      {/* Hidden reason text — referenced by all disabled controls below. */}
      <p id="policies-disabled-reason" className="sr-only">
        Available in Sprint 12 (FUT-003). These controls are not yet
        functional.
      </p>

      {/* Policy cards. */}
      <div className="space-y-4">
        {/* Card 1 — Max token TTL. */}
        <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-surface)] p-6">
          <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
            <div>
              <h2 className="text-sm font-medium">Max token TTL</h2>
              <p className="mt-0.5 text-xs text-[var(--color-fg-muted)]">
                No API key may have a lifetime longer than this value. Keys
                created before the policy is applied are grandfathered until
                they next rotate.
              </p>
            </div>

            <div className="flex shrink-0 items-center gap-3">
              {/* Dummy range slider — disabled. */}
              <input
                type="range"
                min={1}
                max={365}
                defaultValue={90}
                disabled
                aria-disabled="true"
                aria-label="Max token TTL in days"
                aria-describedby="policies-disabled-reason"
                className="w-32 opacity-60 cursor-not-allowed"
              />
              <span className="w-20 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-subtle)] px-3 py-1.5 text-right text-sm opacity-60">
                90 days
              </span>
            </div>
          </div>
        </div>

        {/* Card 2 — Force rotation cadence. */}
        <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-surface)] p-6">
          <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
            <div>
              <h2 className="text-sm font-medium">Force rotation</h2>
              <p className="mt-0.5 text-xs text-[var(--color-fg-muted)]">
                Require every API key to be rotated at least once per this
                interval. Owners receive an email reminder 14 days before
                expiry.
              </p>
            </div>

            <div className="flex shrink-0 items-center gap-2">
              <input
                type="number"
                min={30}
                max={730}
                defaultValue={365}
                disabled
                aria-disabled="true"
                aria-label="Force rotation interval in days"
                aria-describedby="policies-disabled-reason"
                className="w-20 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-surface)] px-3 py-1.5 text-right text-sm opacity-60 cursor-not-allowed"
              />
              <span className="text-sm text-[var(--color-fg-muted)]">days</span>
            </div>
          </div>
        </div>

        {/* Card 3 — Idle revoke. */}
        <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-surface)] p-6">
          <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
            <div>
              <h2 className="text-sm font-medium">Idle revoke</h2>
              <p className="mt-0.5 text-xs text-[var(--color-fg-muted)]">
                Automatically revoke keys that have not been used within this
                window. A warning email is sent 7 days before revocation.
              </p>
            </div>

            <div className="flex shrink-0 items-center gap-2">
              <input
                type="number"
                min={1}
                max={365}
                defaultValue={30}
                disabled
                aria-disabled="true"
                aria-label="Idle revocation threshold in days"
                aria-describedby="policies-disabled-reason"
                className="w-20 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-surface)] px-3 py-1.5 text-right text-sm opacity-60 cursor-not-allowed"
              />
              <span className="text-sm text-[var(--color-fg-muted)]">
                days unused
              </span>
            </div>
          </div>
        </div>
      </div>

      {/* Footer actions — both disabled. */}
      <div className="flex items-center gap-3 pt-2">
        {/* Apply globally. */}
        <button
          type="button"
          disabled
          aria-disabled="true"
          aria-describedby="policies-disabled-reason"
          className="inline-flex items-center rounded-md bg-[var(--color-accent)] px-4 py-2 text-sm font-medium text-white opacity-60 cursor-not-allowed"
        >
          Apply to all keys
        </button>

        {/* Per-key override toggle — shown as a descriptive toggle. */}
        <label
          htmlFor="per-key-override"
          className="flex cursor-not-allowed items-center gap-2 text-sm text-[var(--color-fg-muted)] opacity-60"
          aria-describedby="policies-disabled-reason"
        >
          <input
            id="per-key-override"
            type="checkbox"
            disabled
            aria-disabled="true"
            aria-describedby="policies-disabled-reason"
            className="cursor-not-allowed"
          />
          Allow per-key override
        </label>
      </div>
    </div>
  );
}

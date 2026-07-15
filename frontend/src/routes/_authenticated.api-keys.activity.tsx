import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { cn } from "@/lib/utils";
import { useAuthStore } from "@/lib/auth/store";
import { isWorkspaceAdmin } from "@/lib/auth/jwt";
import { useMe } from "@/lib/api/me";
import { useServiceAccounts } from "@/lib/api/service-accounts";
import { ActivityTable } from "@/components/access/ActivityTable";
import {
  TIME_RANGES,
  DEFAULT_RANGE,
  PAGE_LIMIT_CAP,
  sinceForRange,
  type TimeRangeLabel,
} from "@/lib/activity-range";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

// /api-keys/activity — principal activity feed (FE-API-048 T27).
//
// No admin guard in `beforeLoad` because every authenticated user can view
// their own activity. The principal filter dropdown is the only admin-gated
// element — non-admins see a static "Me (you)" label; admins get a dropdown
// that can target any SA or self.
export const Route = createFileRoute("/_authenticated/api-keys/activity")({
  component: ActivityPage,
});

// Time-range chip options + the window→`since` conversion live in
// @/lib/activity-range. Each window now maps to a real RFC3339 `since` lower
// bound that the auth endpoint threads into the audit query (FUT-088 #1),
// replacing the old limit-as-time-proxy approximation.

// ActivityPage — layout: page header, filter row (principal + time range),
// then the ActivityTable.
function ActivityPage(): React.ReactElement {
  const claims = useAuthStore((s) => s.claims);
  const isAdmin = isWorkspaceAdmin(claims);

  // Self identity — used as both the default principal selection and the
  // "Me (you)" label. `useMe` is already pre-fetched by AppShell so this
  // call hits the TanStack Query cache with no additional network round-trip.
  const { data: me } = useMe();

  // The authenticated user's ID. For humans: user_id. For SA callers: id.
  // We prefer the JWT `sub` as a reliable fallback when the /users/me response
  // has not yet resolved.
  const selfID: string | undefined =
    me?.user_id ?? me?.id ?? claims?.sub ?? undefined;

  // Derive a human-readable display name for the "Me (you)" option.
  const selfLabel: string =
    me?.username ??
    me?.display_name ??
    (claims?.sub ? `${claims.sub.slice(0, 8)}…` : "Me (you)");

  // SA list — fetched only when the caller is an admin, matching the auth
  // guard on the service-accounts API. Non-admin callers receive a 403;
  // we skip the query entirely via `enabled: isAdmin`.
  const saQuery = useServiceAccounts();
  const serviceAccounts = isAdmin ? (saQuery.data ?? []) : [];

  // Selected principal state.
  // "self" is the reserved value meaning the authenticated user's own ID.
  // For admin SA selection the value is the SA's `shadow_user_id`.
  const [selectedPrincipal, setSelectedPrincipal] =
    React.useState<string>("self");

  // Time-range chip state — default 7d.
  const [selectedRange, setSelectedRange] =
    React.useState<TimeRangeLabel>(DEFAULT_RANGE);

  // Derive the initial page size from the selected window.
  const limit =
    TIME_RANGES.find((r) => r.label === selectedRange)?.limit ?? 100;

  // Compute the `since` lower bound for the selected window. Memoized on the
  // window label so it is stable across renders — recomputing Date.now() every
  // render would churn the query key and refetch in a loop. The bound is fixed
  // at the moment the window (re)selects, which is fine for an activity feed.
  const since = React.useMemo(
    () => sinceForRange(selectedRange, Date.now()),
    [selectedRange],
  );

  // "Load more" raises the page size toward the backend cap. Clamped at
  // PAGE_LIMIT_CAP so a limit-based expansion never trips the endpoint's 400 —
  // the bug the old 30d window (limit=500) hit. Beyond the cap the operator
  // narrows the window, which now genuinely shrinks the result set.
  const [extraLimit, setExtraLimit] = React.useState(0);
  const effectiveLimit = Math.min(limit + extraLimit, PAGE_LIMIT_CAP);

  // Reset extra limit whenever the range or principal changes so stale
  // expansions don't bleed across filter changes.
  React.useEffect(() => {
    setExtraLimit(0);
  }, [selectedRange, selectedPrincipal]);

  // Resolve the principalUserID to pass to the table.
  // "self" always maps to the authenticated user's ID.
  const principalUserID: string | undefined =
    selectedPrincipal === "self" ? selfID : selectedPrincipal;

  // Resolve the display name for the selected principal so the table can
  // show it in the Principal column.
  const principalDisplayName: string = React.useMemo(() => {
    if (selectedPrincipal === "self") return selfLabel;
    const sa = serviceAccounts.find(
      (s) => s.shadow_user_id === selectedPrincipal,
    );
    return sa?.name ?? selectedPrincipal.slice(0, 8) + "…";
  }, [selectedPrincipal, selfLabel, serviceAccounts]);

  function handleLoadMore(): void {
    // Increase the limit by the current window step on each "Load more" press.
    const step = limit;
    setExtraLimit((prev) => prev + step);
  }

  return (
    <div className="space-y-6">
      {/* Page header — matches other hub child routes. */}
      <header className="flex flex-col gap-1">
        <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          Access
        </p>
        <h1 className="font-display text-3xl font-medium tracking-tight">
          Activity
        </h1>
        <p className="text-sm text-[var(--color-fg-muted)]">
          Authenticated requests made by API keys and service accounts in this
          workspace. Non-admins see their own feed only.
        </p>
      </header>

      {/* Filter row — principal selector + time-range chips. */}
      <div className="flex flex-wrap items-baseline gap-4">
        {/* Principal filter */}
        <PrincipalFilter
          isAdmin={isAdmin}
          selfLabel={selfLabel}
          serviceAccounts={serviceAccounts}
          value={selectedPrincipal}
          onChange={setSelectedPrincipal}
        />

        {/* Time-range chip selector */}
        <TimeRangeChips value={selectedRange} onChange={setSelectedRange} />
      </div>

      {/* Activity table */}
      <ActivityTable
        principalUserID={principalUserID}
        limit={effectiveLimit}
        since={since}
        principalDisplayName={principalDisplayName}
        onLoadMore={handleLoadMore}
      />
    </div>
  );
}

// ── PrincipalFilter ───────────────────────────────────────────────────────────

interface ServiceAccountOption {
  shadow_user_id: string;
  name: string;
}

interface PrincipalFilterProps {
  isAdmin: boolean;
  selfLabel: string;
  serviceAccounts: ServiceAccountOption[];
  value: string;
  onChange: (id: string) => void;
}

// PrincipalFilter — for non-admins renders a static label ("Me (you)").
// For admins renders a Beacon-themed Radix Select containing "Me (you)" +
// all active SAs. The native <select> previously used here leaked the OS
// theme into the otherwise-Beacon-styled page; the themed primitive in
// components/ui/select.tsx matches dialogs, tabs and the rest of the
// design system.
//
// Human-user listing remains a future capability once a /users list API
// ships — for now the only non-self targets are service accounts.
function PrincipalFilter({
  isAdmin,
  selfLabel,
  serviceAccounts,
  value,
  onChange,
}: PrincipalFilterProps): React.ReactElement {
  // Resolve the selected entry's display label so the closed-state trigger
  // shows the SA name (not its shadow UUID) when an SA is selected.
  const selectedLabel: string =
    value === "self"
      ? `${selfLabel} (you)`
      : (serviceAccounts.find((sa) => sa.shadow_user_id === value)?.name ??
        `${value.slice(0, 8)}…`);

  return (
    <div className="flex items-baseline gap-2">
      <span className="shrink-0 text-xs font-medium uppercase tracking-[0.14em] text-[var(--color-fg-subtle)]">
        Principal
      </span>

      {isAdmin ? (
        <Select value={value} onValueChange={onChange}>
          <SelectTrigger
            aria-label="Select principal"
            className="min-w-[14rem]"
          >
            {/* SelectValue would render the raw option text; we control the
                label explicitly so the closed-state can show "<name> (you)"
                for the self entry without forcing that suffix into the
                option list. */}
            <SelectValue placeholder="Select principal">
              {selectedLabel}
            </SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="self">{selfLabel} (you)</SelectItem>
            {serviceAccounts.length > 0 ? (
              <SelectGroup>
                <SelectLabel>Service accounts</SelectLabel>
                {serviceAccounts.map((sa) => (
                  <SelectItem key={sa.shadow_user_id} value={sa.shadow_user_id}>
                    {sa.name}
                  </SelectItem>
                ))}
              </SelectGroup>
            ) : null}
          </SelectContent>
        </Select>
      ) : (
        /* Non-admin: static label — no dropdown, always scoped to self. */
        <span className="text-sm text-[var(--color-fg)]">
          {selfLabel} (you)
        </span>
      )}
    </div>
  );
}

// ── TimeRangeChips ────────────────────────────────────────────────────────────

interface TimeRangeChipsProps {
  value: TimeRangeLabel;
  onChange: (v: TimeRangeLabel) => void;
}

// TimeRangeChips — a segmented pill row for 24h / 7d / 30d.
// The active chip gets the accent background; others are muted outlines.
function TimeRangeChips({
  value,
  onChange,
}: TimeRangeChipsProps): React.ReactElement {
  return (
    <div className="flex items-baseline gap-2">
      <span className="shrink-0 text-xs font-medium uppercase tracking-[0.14em] text-[var(--color-fg-subtle)]">
        Range
      </span>
      <div className="flex gap-1" role="group" aria-label="Time range">
        {TIME_RANGES.map((r) => {
          const active = r.label === value;
          return (
            <button
              key={r.label}
              type="button"
              onClick={() => onChange(r.label)}
              className={cn(
                "rounded-full border px-3 py-0.5 text-xs font-medium transition-colors",
                active
                  ? "border-[var(--color-accent-border)] bg-[var(--color-accent-subtle)] text-[var(--color-accent)]"
                  : "border-[var(--color-border)] bg-transparent text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-sunken)]",
              )}
              aria-pressed={active}
            >
              {r.label}
            </button>
          );
        })}
      </div>
    </div>
  );
}

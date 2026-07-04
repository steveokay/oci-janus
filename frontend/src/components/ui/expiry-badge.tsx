import * as React from "react";
import { Badge } from "@/components/ui/badge";
import { formatAbsoluteDate, formatRelativeDate } from "@/lib/format";

// ExpiryBadge — shared urgency treatment for API-key expiry timestamps.
// Used by both the personal API-keys table (/profile) and the service-account
// key table so a "key about to expire" reads the same everywhere.
//
// Urgency tiers (see expiryUrgency below):
//   none    → no expiry set        → muted "Never"
//   expired → expires_at <= now    → danger badge "Expired"
//   soon    → within EXPIRY_SOON_DAYS → warning badge + relative time
//   ok      → further out          → plain muted relative time
//
// The tier logic is factored into the pure `expiryUrgency` helper so it can be
// unit-tested without rendering (thresholds are easy to get wrong at the
// boundary).

// Keys expiring within this window get the warning treatment. 14 days is a
// typical "rotate it this sprint" horizon for CI credentials.
export const EXPIRY_SOON_DAYS = 14;

export type ExpiryUrgency = "none" | "expired" | "soon" | "ok";

// expiryUrgency classifies an expiry timestamp relative to `now`. Pure +
// side-effect-free so it's unit-testable. `now` is injectable for tests; it
// defaults to the current time in normal use.
//
// Returns "none" for absent/unparseable input so a missing expiry never
// masquerades as expired.
export function expiryUrgency(
  iso: string | null | undefined,
  now: number = Date.now(),
): ExpiryUrgency {
  if (!iso) return "none";
  const ts = Date.parse(iso);
  if (Number.isNaN(ts)) return "none";
  if (ts <= now) return "expired";
  // Half-open window: [now, now + 14d) is "soon"; exactly 14d out is still ok.
  const soonCutoff = now + EXPIRY_SOON_DAYS * 24 * 60 * 60 * 1000;
  if (ts < soonCutoff) return "soon";
  return "ok";
}

interface ExpiryBadgeProps {
  // The key's expiry timestamp (ISO 8601), or null/undefined when the key
  // never expires.
  expiresAt: string | null | undefined;
  className?: string;
}

export function ExpiryBadge({
  expiresAt,
  className,
}: ExpiryBadgeProps): React.ReactElement {
  const urgency = expiryUrgency(expiresAt);

  // No expiry — the key is perpetual. Muted "Never" matches the prior copy
  // both tables used before the urgency treatment was added.
  if (urgency === "none") {
    return (
      <span className="text-xs text-[var(--color-fg-subtle)]">Never</span>
    );
  }

  // Expired — danger badge. We drop the relative time here because "Expired"
  // is the actionable signal; the exact time still rides in the title tooltip.
  if (urgency === "expired") {
    return (
      <Badge
        tone="danger"
        className={className}
        title={formatAbsoluteDate(expiresAt)}
      >
        Expired
      </Badge>
    );
  }

  // Expiring soon — warning badge with the relative countdown ("in 3 days").
  if (urgency === "soon") {
    return (
      <Badge
        tone="warning"
        className={className}
        title={formatAbsoluteDate(expiresAt)}
      >
        {formatRelativeDate(expiresAt)}
      </Badge>
    );
  }

  // Comfortably in the future — plain muted relative time, no badge chrome.
  return (
    <span
      className="text-xs text-[var(--color-fg-muted)]"
      title={formatAbsoluteDate(expiresAt)}
    >
      {formatRelativeDate(expiresAt)}
    </span>
  );
}

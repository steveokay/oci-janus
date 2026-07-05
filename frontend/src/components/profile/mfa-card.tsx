import * as React from "react";
import { ShieldCheck, ShieldOff } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { useMfaStatus } from "@/lib/api/mfa";
import { formatAbsoluteDate, formatRelativeDate } from "@/lib/format";

interface MfaCardProps {
  // Opens the enrolment dialog (owned by the route, like the password dialog).
  onEnroll: () => void;
  // Opens the disable flow (dialog built in the next task).
  onDisable: () => void;
}

// Beacon — MfaCard.
//
// Status surface for TOTP two-factor auth on Settings › Account. Reads
// useMfaStatus and renders one of three states: loading skeleton, an
// "enabled" state (enrolled date + Disable), or a "not enabled" explainer
// with an Enable CTA. The actual enrol/disable dialogs are owned by the
// route so this card stays a dumb emitter (mirrors IdentityCard).
export function MfaCard({
  onEnroll,
  onDisable,
}: MfaCardProps): React.ReactElement {
  const { data, isLoading, isError } = useMfaStatus();

  // Match IdentityCard's inline error treatment — a danger-accented card
  // rather than a full-page error state, since this is one section of many.
  if (isError) {
    return (
      <Card accentBar="danger">
        <CardContent>
          <p className="py-2 text-sm text-[var(--color-danger)]">
            Couldn't load two-factor status. Retry from a fresh session if this
            persists.
          </p>
        </CardContent>
      </Card>
    );
  }

  const enabled = data?.enabled ?? false;

  return (
    <Card accentBar={enabled ? "success" : "accent"}>
      <CardHeader>
        <div className="flex items-center justify-between">
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Two-factor authentication
          </CardDescription>
          {isLoading || !data ? (
            <Skeleton className="h-5 w-16 rounded-full" />
          ) : enabled ? (
            <Badge tone="success">
              <ShieldCheck className="size-3" /> Enabled
            </Badge>
          ) : (
            <Badge tone="neutral">
              <ShieldOff className="size-3" /> Off
            </Badge>
          )}
        </div>
      </CardHeader>
      <CardContent>
        {isLoading || !data ? (
          <Skeleton className="h-16 w-full" />
        ) : enabled ? (
          // Enabled state — show when they enrolled + a Disable action.
          <div className="flex items-center justify-between gap-4">
            <p className="text-sm text-[var(--color-fg-muted)]">
              Your account is protected with an authenticator app.
              {data.enrolled_at ? (
                <>
                  {" "}
                  Enrolled {formatRelativeDate(data.enrolled_at)}
                  <span className="text-[var(--color-fg-subtle)]">
                    {" "}
                    · {formatAbsoluteDate(data.enrolled_at)}
                  </span>
                  .
                </>
              ) : null}
            </p>
            <Button
              variant="outline"
              size="sm"
              onClick={onDisable}
              className="shrink-0"
            >
              Disable
            </Button>
          </div>
        ) : (
          // Not-enabled state — short explainer + Enable CTA.
          <div className="flex items-center justify-between gap-4">
            <p className="max-w-prose text-sm text-[var(--color-fg-muted)]">
              Add a second factor with a TOTP authenticator app. You'll enter a
              6-digit code at sign-in on top of your password, and get one-time
              backup codes in case you lose your device.
            </p>
            <Button size="sm" onClick={onEnroll} className="shrink-0">
              <ShieldCheck className="size-3.5" />
              Enable two-factor authentication
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

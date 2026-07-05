import React from "react";
import { toast } from "sonner";
import { useNavigate } from "@tanstack/react-router";
import { Monitor, LogOut } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { ConfirmDestructiveDialog } from "@/components/ui/confirm-destructive-dialog";
import { formatAbsoluteDate, formatRelativeDate } from "@/lib/format";
import { authStore } from "@/lib/auth/store";
import {
  useSessions,
  useRevokeSession,
  useRevokeOtherSessions,
  type Session,
} from "@/lib/api/sessions";

// Beacon — SessionsCard (Tier-1 #1 session management).
//
// Lists the account's active sign-in sessions on Settings › Account and lets
// the operator revoke any one of them (or sign out every *other* device in one
// click). Revoking the CURRENT session is a self-logout: the backend kills the
// sid, so we clear the in-memory auth store and bounce to /login rather than
// waiting for the next request to 401.
export function SessionsCard(): React.ReactElement {
  const { data, isLoading, isError, refetch } = useSessions();
  const revoke = useRevokeSession();
  const revokeOthers = useRevokeOtherSessions();
  const navigate = useNavigate();
  // The session the operator has queued for revocation (drives the confirm
  // dialog). null == no dialog open.
  const [target, setTarget] = React.useState<Session | null>(null);
  // Separate busy flag for the confirm dialog's spinner — mutateAsync's own
  // isPending would also flip during the "sign out others" flow, so we track
  // this interaction explicitly.
  const [busy, setBusy] = React.useState(false);

  async function confirmRevoke(): Promise<void> {
    if (!target) return;
    setBusy(true);
    try {
      await revoke.mutateAsync(target.sid);
      if (target.current) {
        // Revoked our own session — self-logout. Clear the token and leave
        // before any further authenticated request can 401.
        toast.success("Signed out.");
        authStore.clear();
        void navigate({ to: "/login", replace: true });
        return;
      }
      toast.success("Session revoked.");
      setTarget(null);
    } catch {
      toast.error("Couldn't revoke the session. Try again.");
    } finally {
      setBusy(false);
    }
  }

  async function signOutOthers(): Promise<void> {
    try {
      const n = await revokeOthers.mutateAsync();
      toast.success(
        n > 0
          ? `Signed out ${n} other session${n === 1 ? "" : "s"}.`
          : "No other sessions.",
      );
    } catch {
      toast.error("Couldn't sign out other sessions. Try again.");
    }
  }

  const sessions = data ?? [];
  // "Sign out others" only makes sense when there's at least one non-current
  // session to revoke.
  const hasOthers = sessions.some((s) => !s.current);

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between">
          <div>
            <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              Active sessions
            </CardDescription>
            <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
              Devices currently signed in to your account.
            </p>
          </div>
          {hasOthers ? (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => void signOutOthers()}
            >
              <LogOut className="size-3.5" />
              Sign out others
            </Button>
          ) : null}
        </div>
      </CardHeader>
      <CardContent>
        {isError ? (
          <ErrorState
            title="Couldn't load sessions"
            description="Something went wrong fetching your sessions."
            onRetry={() => void refetch()}
          />
        ) : !isLoading && sessions.length === 0 ? (
          <EmptyState
            icon={<Monitor className="size-5" />}
            title="No active sessions"
            description="You have no other signed-in devices."
          />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Device</TableHead>
                <TableHead>IP</TableHead>
                <TableHead>Last active</TableHead>
                <TableHead className="text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {isLoading
                ? // Two skeleton rows while the list loads.
                  [0, 1].map((i) => (
                    <TableRow key={i}>
                      <TableCell>
                        <Skeleton className="h-3 w-40" />
                      </TableCell>
                      <TableCell>
                        <Skeleton className="h-3 w-24" />
                      </TableCell>
                      <TableCell>
                        <Skeleton className="h-3 w-20" />
                      </TableCell>
                      <TableCell />
                    </TableRow>
                  ))
                : sessions.map((s) => (
                    <TableRow key={s.sid}>
                      <TableCell>
                        {/* Full UA string on hover; short label inline. */}
                        <span
                          className="text-sm text-[var(--color-fg)]"
                          title={s.user_agent}
                        >
                          {s.device_label}
                        </span>
                        {s.current ? (
                          <Badge tone="success" className="ml-2">
                            This device
                          </Badge>
                        ) : null}
                      </TableCell>
                      <TableCell className="font-mono text-xs text-[var(--color-fg-muted)]">
                        {s.ip}
                      </TableCell>
                      <TableCell>
                        <span
                          className="text-xs text-[var(--color-fg)]"
                          title={formatAbsoluteDate(s.last_active_at)}
                        >
                          {formatRelativeDate(s.last_active_at)}
                        </span>
                      </TableCell>
                      <TableCell className="text-right">
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setTarget(s)}
                          className="text-[var(--color-danger)] hover:bg-[var(--color-danger)]/10"
                        >
                          {/* Current session reads as "Sign out"; others as */}
                          {/* "Revoke" since you're killing someone else's. */}
                          {s.current ? "Sign out" : "Revoke"}
                        </Button>
                      </TableCell>
                    </TableRow>
                  ))}
            </TableBody>
          </Table>
        )}
      </CardContent>

      {/* Single low-severity confirm dialog reused for every row. Copy + label */}
      {/* swap on whether the target is the current device. */}
      <ConfirmDestructiveDialog
        open={target !== null}
        onOpenChange={(o) => {
          if (!o) setTarget(null);
        }}
        severity="low"
        title={target?.current ? "Sign out this device?" : "Revoke this session?"}
        description={
          target?.current
            ? "You'll be signed out of this device immediately."
            : `Revoke the session on ${
                target?.device_label ?? "this device"
              }? That device will be signed out.`
        }
        confirmLabel={target?.current ? "Sign out" : "Revoke"}
        loading={busy}
        onConfirm={confirmRevoke}
      />
    </Card>
  );
}

import * as React from "react";
import { toast } from "sonner";
import { AxiosError } from "axios";
import { Lock, Unlock } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Button } from "@/components/ui/button";
import { useManifest } from "@/lib/api/manifest";
import { useLiftQuarantine } from "@/lib/api/quarantine";
import { formatAbsoluteDate, formatRelativeDate } from "@/lib/format";

// Beacon — QuarantineBanner (FE-API-050).
//
// Renders above the ScanPanel on the tag-detail Security tab when the
// manifest pointed at by this tag is quarantined. Pull requests of
// this tag currently return 451 from registry-core. The banner
// surfaces:
//
//   - the scanner-supplied reason ("scan blocked by policy: 3 CRITICAL,
//     2 HIGH")
//   - who quarantined ("scanner" for automatic, user_id for manual)
//   - when (relative + absolute)
//   - a "Lift quarantine" action — type-to-confirm dialog so an
//     accidental click doesn't bypass the security gate
//
// Repo admin/owner is enforced server-side; sub-admins see a 403 toast
// instead of the lift succeeding silently.
//
// Renders nothing when the manifest isn't quarantined (the common
// case) so the component is safe to mount unconditionally on the
// Security tab.

interface QuarantineBannerProps {
  org: string;
  repo: string;
  tag: string;
}

export function QuarantineBanner({
  org,
  repo,
  tag,
}: QuarantineBannerProps): React.ReactElement | null {
  const { data } = useManifest(org, repo, tag);
  const [open, setOpen] = React.useState(false);

  if (!data || !data.quarantined) return null;

  return (
    <>
      <div
        className="rounded-md border border-[var(--color-danger)]/40 bg-[var(--color-danger)]/10 p-4"
        role="alert"
      >
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="flex items-start gap-3">
            <Lock
              className="size-4 mt-0.5 shrink-0 text-[var(--color-danger)]"
              aria-hidden
            />
            <div className="space-y-1">
              <div className="text-sm font-medium text-[var(--color-fg)]">
                Quarantined by scan policy
              </div>
              <p className="text-xs text-[var(--color-fg-muted)]">
                Pull requests of this tag currently return{" "}
                <span className="font-mono">451 Unavailable For Legal Reasons</span>{" "}
                until an admin lifts the gate. Operators can review the scan
                findings below before deciding.
              </p>
              {data.quarantine_reason ? (
                <p className="text-xs text-[var(--color-fg)]">
                  <span className="font-medium">Reason: </span>
                  <span className="font-mono">{data.quarantine_reason}</span>
                </p>
              ) : null}
              <p className="text-[11px] text-[var(--color-fg-subtle)]">
                {data.quarantined_at ? (
                  <>
                    Quarantined{" "}
                    <span title={formatAbsoluteDate(data.quarantined_at)}>
                      {formatRelativeDate(data.quarantined_at)}
                    </span>
                  </>
                ) : (
                  <>Quarantined recently</>
                )}
                {data.quarantined_by ? (
                  <>
                    {" "}
                    by{" "}
                    <span className="font-mono">
                      {data.quarantined_by === "scanner"
                        ? "scanner (automatic)"
                        : data.quarantined_by.slice(0, 8)}
                    </span>
                  </>
                ) : null}
              </p>
            </div>
          </div>
          <Button
            variant="danger"
            size="sm"
            onClick={() => setOpen(true)}
          >
            <Unlock className="size-3.5" />
            Lift quarantine
          </Button>
        </div>
      </div>

      <LiftQuarantineDialog
        open={open}
        onOpenChange={setOpen}
        org={org}
        repo={repo}
        tag={tag}
      />
    </>
  );
}

// LiftQuarantineDialog — type-to-confirm gate on the lift action.
// Operator has to type the tag name to confirm; matches the existing
// destructive-flow pattern used by repo delete + webhook delete +
// tenant delete. Lifting is REVERSIBLE (the next scan will re-stamp
// quarantine if findings still violate), but the gate exists for a
// reason and we want one extra deliberate step before bypassing it.

function LiftQuarantineDialog({
  open,
  onOpenChange,
  org,
  repo,
  tag,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  org: string;
  repo: string;
  tag: string;
}): React.ReactElement {
  const [confirmText, setConfirmText] = React.useState("");
  const lift = useLiftQuarantine();

  // Reset the confirm field whenever the dialog opens / closes so a
  // previous attempt's text doesn't leak between opens.
  React.useEffect(() => {
    if (!open) setConfirmText("");
  }, [open]);

  const canLift = confirmText === tag && !lift.isPending;

  async function onConfirm(): Promise<void> {
    try {
      await lift.mutateAsync({ org, repo, tag });
      toast.success(`Quarantine lifted on ${tag}.`);
      onOpenChange(false);
    } catch (e) {
      const status = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        status === 403
          ? "Admin or owner on this repo is required to lift."
          : status === 404
            ? "Tag or repository not found."
            : "Couldn't lift the quarantine. Try again, or check the BFF logs.",
      );
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Unlock className="size-4 text-[var(--color-danger)]" />
            Lift quarantine
          </DialogTitle>
          <DialogDescription>
            Lifting the quarantine restores pull access for this tag.
            Operators in this tenant will be able to pull the image
            immediately. The next scan will re-stamp quarantine if
            findings still violate the configured policy.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-2">
          <Label htmlFor="lift-confirm">
            Type{" "}
            <span className="font-mono text-[var(--color-fg)]">{tag}</span>{" "}
            to confirm
          </Label>
          <Input
            id="lift-confirm"
            value={confirmText}
            onChange={(e) => setConfirmText(e.target.value)}
            placeholder={tag}
            className="font-mono"
            autoComplete="off"
            spellCheck={false}
          />
        </div>

        <DialogFooter>
          <Button
            variant="ghost"
            onClick={() => onOpenChange(false)}
            disabled={lift.isPending}
          >
            Cancel
          </Button>
          <Button
            variant="danger"
            onClick={onConfirm}
            disabled={!canLift}
            loading={lift.isPending}
          >
            {lift.isPending ? "Lifting" : "Lift quarantine"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

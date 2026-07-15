import * as React from "react";
import { ShieldAlert } from "lucide-react";
import { toast } from "sonner";
import { AxiosError } from "axios";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { ConfirmDestructiveDialog } from "@/components/ui/confirm-destructive-dialog";
import { useBulkScanOrg } from "@/lib/api/scan";

// OrgBulkScanSection — org-wide "scan every repository" action on the org
// Settings page (FUT-088 paper-cut #5). The `useBulkScanOrg` hook + its BFF
// route (`POST /orgs/{org}/scan`) already existed but no UI ever imported it,
// so the org-level bulk scan was reachable only via curl. This card surfaces
// it next to the org-default policy editors, mirroring the repo-level bulk
// scan that already ships in the tags panel.
//
// It's a heavy, fan-out action (every image tag in every repo), so it rides
// the same type-to-confirm gate (severity="high", phrase "SCAN") the repo
// version uses. The BFF gates on org admin; a non-admin sees the 403 surfaced
// as a toast rather than a hidden button, matching the repo pattern.

interface OrgBulkScanSectionProps {
  org: string;
}

export function OrgBulkScanSection({
  org,
}: OrgBulkScanSectionProps): React.ReactElement {
  const [open, setOpen] = React.useState(false);
  const mutation = useBulkScanOrg();

  async function handleConfirm(): Promise<void> {
    try {
      const res = await mutation.mutateAsync({ org });
      const cappedSuffix = res.capped
        ? ` · capped at ${res.limit.toLocaleString()} — run again to continue`
        : "";
      toast.success(
        `Queued ${res.scans_queued.toLocaleString()} scans across ` +
          `${res.repositories_count.toLocaleString()} ${res.repositories_count === 1 ? "repository" : "repositories"}${cappedSuffix}`,
      );
      setOpen(false);
    } catch (e) {
      const code = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        code === 403
          ? "Org admin role required to scan every repository."
          : code === 404
            ? "Organization not found."
            : "Couldn't queue the bulk scan. Check the BFF logs.",
      );
    }
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-start gap-2">
          <ShieldAlert className="mt-0.5 size-4 shrink-0 text-[var(--color-fg-subtle)]" />
          <div className="space-y-1">
            <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              Scan all repositories
            </CardDescription>
            <p className="text-xs text-[var(--color-fg-muted)]">
              Queue a vulnerability scan for every image tag in every repository
              under <code className="font-mono">{org}</code>. Non-image
              artifacts (Helm charts, signatures, SBOMs) are skipped
              automatically. The server caps each request; run again if the
              toast says it hit the cap.
            </p>
          </div>
        </div>
      </CardHeader>
      <CardContent className="pt-0">
        <div className="flex justify-end">
          <Button
            size="sm"
            variant="outline"
            disabled={mutation.isPending}
            onClick={() => setOpen(true)}
          >
            Scan all repositories…
          </Button>
        </div>
      </CardContent>

      <ConfirmDestructiveDialog
        open={open}
        onOpenChange={setOpen}
        severity="high"
        confirmPhrase="SCAN"
        title={`Scan every repository in ${org}`}
        confirmLabel="Queue scans"
        loading={mutation.isPending}
        onConfirm={handleConfirm}
        description={
          <>
            Queues a vulnerability scan for every image tag in every repository
            under <code className="font-mono">{org}</code>. This can be a large
            fan-out on a busy org; the server caps each request and the toast
            reports if it did.
          </>
        }
      />
    </Card>
  );
}

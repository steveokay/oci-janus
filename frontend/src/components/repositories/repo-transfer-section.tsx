import * as React from "react";
import { useNavigate } from "@tanstack/react-router";
import { ArrowRightLeft } from "lucide-react";
import { toast } from "sonner";
import { AxiosError } from "axios";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useTransferRepository } from "@/lib/api/repositories";
import { useOrgs } from "@/lib/api/orgs";

// RepoTransferSection — General-section card on the repo Settings tab (repo
// transfer feature, PR D). Moves the repo to a different org via the dedicated
// POST /repositories/{org}/{repo}/transfer route, which also migrates the
// repo-scoped RBAC grants in registry-auth.
//
// Transfer changes the repo's org (and URL), so it is guarded by a confirm
// dialog and, on success, navigates to the new location. The BFF returns a
// partial-success `rbac_warning` when the transfer committed but the scope
// rewrite failed — surfaced as a warning toast.

interface RepoTransferSectionProps {
  org: string;
  repo: string;
}

export function RepoTransferSection({
  org,
  repo,
}: RepoTransferSectionProps): React.ReactElement {
  const navigate = useNavigate();
  const transfer = useTransferRepository();
  const { data: orgsData, isLoading: orgsLoading } = useOrgs();

  const [destOrg, setDestOrg] = React.useState("");
  const [confirmOpen, setConfirmOpen] = React.useState(false);

  // Candidate destinations are every org except the current one. The BFF
  // still enforces admin-on-dest, so a non-admin picking an org here gets a
  // 403 — the list is a convenience, not the authorization boundary.
  const destOptions = (orgsData?.orgs ?? [])
    .map((o) => o.org)
    .filter((name) => name !== org);

  const canSubmit = destOrg !== "" && destOrg !== org;

  async function doTransfer(): Promise<void> {
    try {
      const res = await transfer.mutateAsync({ org, repo, dest_org: destOrg });
      setConfirmOpen(false);
      if (res.rbac_warning) {
        toast.warning(res.rbac_warning);
      } else {
        toast.success(
          `Transferred to ${destOrg}/${repo}` +
            (res.roles_rewritten > 0
              ? ` · ${res.roles_rewritten} access grant${res.roles_rewritten === 1 ? "" : "s"} migrated`
              : ""),
        );
      }
      // Follow the repo to its new org.
      void navigate({
        to: "/repositories/$org/$repo",
        params: { org: destOrg, repo },
      });
    } catch (e) {
      const code = (e as AxiosError | undefined)?.response?.status;
      const message =
        code === 403
          ? "Admin role required on both this repository and the destination org."
          : code === 404
            ? "Destination organization not found."
            : code === 409
              ? "A repository with that name already exists in the destination org."
              : "Couldn't transfer the repository. Check the BFF logs.";
      toast.error(message);
    }
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-start gap-2">
          <ArrowRightLeft className="mt-0.5 size-4 shrink-0 text-[var(--color-fg-subtle)]" />
          <div className="space-y-1">
            <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              Transfer
            </CardDescription>
            <p className="text-xs text-[var(--color-fg-muted)]">
              Move this repository to another organization. Existing tags and
              manifests are preserved; pull URLs change. Requires admin on both
              this repo and the destination org.
            </p>
          </div>
        </div>
      </CardHeader>
      <CardContent className="pt-0 space-y-3">
        <Select
          value={destOrg}
          onValueChange={setDestOrg}
          disabled={transfer.isPending || orgsLoading}
        >
          <SelectTrigger aria-label="Destination organization">
            <SelectValue
              placeholder={
                orgsLoading ? "Loading organizations…" : "Select destination org…"
              }
            />
          </SelectTrigger>
          <SelectContent>
            {destOptions.map((name) => (
              <SelectItem key={name} value={name}>
                {name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {!orgsLoading && destOptions.length === 0 && (
          <p className="text-[11px] text-[var(--color-fg-subtle)]">
            No other organizations to transfer into.
          </p>
        )}
        <div className="flex justify-end">
          <Button
            size="sm"
            disabled={!canSubmit || transfer.isPending}
            onClick={() => setConfirmOpen(true)}
          >
            Transfer…
          </Button>
        </div>
      </CardContent>

      <Dialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Transfer repository</DialogTitle>
            <DialogDescription asChild>
              <div className="space-y-2">
                <p>
                  Move <code className="font-mono">{org}/{repo}</code> to{" "}
                  <code className="font-mono">{destOrg}/{repo}</code>?
                </p>
                <p>
                  Access grants scoped to this repo migrate to the new org.
                  Anyone pulling by the old path will get a 404 until they
                  update their references. Stored images are not affected.
                </p>
              </div>
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="ghost"
              onClick={() => setConfirmOpen(false)}
              disabled={transfer.isPending}
            >
              Cancel
            </Button>
            <Button
              onClick={() => void doTransfer()}
              disabled={transfer.isPending}
            >
              {transfer.isPending ? "Transferring…" : "Transfer repository"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}

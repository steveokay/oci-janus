import * as React from "react";
import { toast } from "sonner";
import {
  CheckCircle2,
  Clock,
  Globe,
  RefreshCw,
  ShieldCheck,
  Star,
  Trash2,
} from "lucide-react";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  useDeleteDomain,
  usePromoteDomain,
  useVerifyDomain,
  type DomainEntry,
} from "@/lib/api/domains";
import { formatAbsoluteDate, formatRelativeDate } from "@/lib/format";

interface DomainsTableProps {
  domains: DomainEntry[];
}

// DomainsTable — wraps the four mutation hooks (verify / promote / delete)
// with row-level affordances + a delete-confirmation dialog. Register lives
// in a separate dialog because it's a multi-step flow (form → challenge).
export function DomainsTable({ domains }: DomainsTableProps): React.ReactElement {
  const [deleteTarget, setDeleteTarget] = React.useState<DomainEntry | null>(null);

  return (
    <>
      <div className="overflow-hidden rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-card)]">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Domain</TableHead>
              <TableHead>Status</TableHead>
              <TableHead className="hidden lg:table-cell">Registered</TableHead>
              <TableHead className="w-[280px] text-right">
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {domains.map((d) => (
              <DomainRow
                key={d.domain}
                d={d}
                onDelete={() => setDeleteTarget(d)}
              />
            ))}
          </TableBody>
        </Table>
      </div>

      {deleteTarget ? (
        <DeleteDomainDialog
          domain={deleteTarget}
          open
          onOpenChange={(o) => {
            if (!o) setDeleteTarget(null);
          }}
        />
      ) : null}
    </>
  );
}

function DomainRow({
  d,
  onDelete,
}: {
  d: DomainEntry;
  onDelete: () => void;
}): React.ReactElement {
  const verify = useVerifyDomain();
  const promote = usePromoteDomain();

  async function handleVerify() {
    try {
      const res = await verify.mutateAsync(d.domain);
      toast[res.verified ? "success" : "message"](
        res.verified
          ? "Domain verified."
          : "Verification still pending — TXT record not visible yet.",
      );
    } catch (e) {
      const status = (e as { response?: { status?: number } })?.response?.status;
      toast.error(
        status === 403
          ? "Tenant admin role required."
          : "Couldn't run verification. Try again.",
      );
    }
  }

  async function handlePromote() {
    try {
      await promote.mutateAsync(d.domain);
      toast.success(`${d.domain} is now the primary host.`);
    } catch (e) {
      const status = (e as { response?: { status?: number } })?.response?.status;
      toast.error(
        status === 400
          ? "Backend refused — only verified domains can be promoted."
          : status === 403
            ? "Tenant admin role required."
            : "Couldn't promote. Try again.",
      );
    }
  }

  return (
    <TableRow>
      <TableCell>
        <div className="flex items-center gap-2">
          <Globe className="size-3.5 text-[var(--color-fg-subtle)]" />
          <code className="font-mono text-sm font-medium text-[var(--color-fg)]">
            {d.domain}
          </code>
        </div>
      </TableCell>
      <TableCell>
        <div className="flex flex-wrap items-center gap-1.5">
          {d.is_primary ? (
            <Badge tone="accent">
              <Star className="size-3" />
              Primary
            </Badge>
          ) : null}
          {d.verified ? (
            <Badge tone="success">
              <ShieldCheck className="size-3" /> Verified
            </Badge>
          ) : (
            <Badge tone="warning">
              <Clock className="size-3" /> Pending
            </Badge>
          )}
          {!d.verified && d.notified_48h ? (
            <Badge tone="danger" className="!py-0 text-[10px]">
              48h stalled
            </Badge>
          ) : null}
        </div>
        {!d.verified && d.next_poll_after ? (
          <div className="mt-0.5 text-[11px] text-[var(--color-fg-subtle)]">
            Next poll {formatRelativeDate(d.next_poll_after)}
          </div>
        ) : null}
      </TableCell>
      <TableCell className="hidden text-xs text-[var(--color-fg-muted)] lg:table-cell">
        <span title={formatAbsoluteDate(d.registered_at)}>
          {formatRelativeDate(d.registered_at)}
        </span>
      </TableCell>
      <TableCell className="text-right">
        <div className="flex items-center justify-end gap-1">
          {!d.verified ? (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => void handleVerify()}
              loading={verify.isPending}
              disabled={verify.isPending}
            >
              <RefreshCw className="size-3.5" />
              Verify now
            </Button>
          ) : !d.is_primary ? (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => void handlePromote()}
              loading={promote.isPending}
              disabled={promote.isPending}
            >
              <Star className="size-3.5" />
              Make primary
            </Button>
          ) : null}
          <Button
            variant="ghost"
            size="sm"
            onClick={onDelete}
            className="text-[var(--color-danger)] hover:bg-[var(--color-danger)]/10"
            title={
              d.is_primary
                ? "Deleting the primary domain falls the workspace back to the platform-derived host"
                : ""
            }
          >
            <Trash2 className="size-3.5" />
            Delete
          </Button>
        </div>
      </TableCell>
    </TableRow>
  );
}

function DeleteDomainDialog({
  domain,
  open,
  onOpenChange,
}: {
  domain: DomainEntry;
  open: boolean;
  onOpenChange: (o: boolean) => void;
}): React.ReactElement {
  const del = useDeleteDomain();
  async function handleConfirm() {
    try {
      const headers = await del.mutateAsync(domain.domain);
      // The BFF passes the tenant-service "x-janus-was-primary" gRPC
      // metadata through as the X-Janus-Warning HTTP header. Surface
      // it on the success toast so the operator sees that the primary
      // fell back to the platform host (rather than silently losing
      // their custom hostname).
      const wasPrimaryWarning = headers?.["x-janus-warning"] === "primary-domain-removed";
      toast.success(
        wasPrimaryWarning
          ? `Removed ${domain.domain}. Workspace primary fell back to the platform-derived host.`
          : `Removed ${domain.domain}.`,
      );
      onOpenChange(false);
    } catch {
      toast.error("Couldn't delete. Try again.");
    }
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Delete custom domain</DialogTitle>
          <DialogDescription>
            {domain.is_primary ? (
              <>
                <strong>This is your workspace&apos;s primary domain.</strong>{" "}
                Removing it falls the workspace back to the platform-derived
                host. Docker clients pointing at{" "}
                <code className="font-mono">{domain.domain}</code> will stop
                resolving until DNS is repointed. Continue?
              </>
            ) : (
              <>
                This removes <code className="font-mono">{domain.domain}</code>{" "}
                from this workspace. The platform-derived host stays available,
                so docker clients still resolve — just not via your custom
                hostname.
              </>
            )}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={del.isPending}
          >
            Cancel
          </Button>
          <Button
            type="button"
            variant="danger"
            onClick={() => void handleConfirm()}
            loading={del.isPending}
            disabled={del.isPending}
          >
            <CheckCircle2 className="size-4" />
            Delete domain
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

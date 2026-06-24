import * as React from "react";
import { toast } from "sonner";
import {
  CheckCircle2,
  ChevronDown,
  ChevronRight,
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
import { CopyButton } from "@/components/ui/copy-button";
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
import {
  formatAbsoluteDate,
  formatCountdown,
  formatRelativeDate,
} from "@/lib/format";

interface DomainsTableProps {
  domains: DomainEntry[];
}

// DomainsTable — wraps the four mutation hooks (verify / promote / delete)
// with row-level affordances + a delete-confirmation dialog. Register lives
// in a separate dialog because it's a multi-step flow (form → challenge).
//
// Unverified rows get an expander chevron (DSGN-021) revealing the TXT
// challenge — the same TXT name + value the register dialog hands back at
// creation time. Lets an operator who closed the register dialog still
// compare what they pasted into Cloudflare / Route53 against what the
// registry expects, without having to re-register the domain.
export function DomainsTable({ domains }: DomainsTableProps): React.ReactElement {
  const [deleteTarget, setDeleteTarget] = React.useState<DomainEntry | null>(null);

  return (
    <>
      <div className="overflow-hidden rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-card)]">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-[40px]">
                <span className="sr-only">Expand</span>
              </TableHead>
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
  const [expanded, setExpanded] = React.useState(false);
  // Inline result panel state — populated by handleCheckDNS so the operator
  // can see "Found! Verifying…" vs "Still pending — checked at <time>" without
  // having to consult the row badge. Cleared on every fresh recheck so the
  // panel never shows stale state alongside a spinning button.
  const [lastCheck, setLastCheck] = React.useState<{
    verified: boolean;
    at: Date;
  } | null>(null);

  const canExpand = !d.verified;

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

  // handleCheckDNS — the in-panel variant of "Verify now". Shares the same
  // hook so the row badge + workspace cache invalidation happen identically;
  // the difference is purely UX (inline result line under the value, no toast
  // for the "still pending" case since the operator is staring at the panel).
  async function handleCheckDNS() {
    setLastCheck(null);
    try {
      const res = await verify.mutateAsync(d.domain);
      setLastCheck({ verified: res.verified, at: new Date() });
      if (res.verified) {
        toast.success("Domain verified.");
      }
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
    <>
      <TableRow>
        <TableCell className="w-[40px] pr-0">
          {canExpand ? (
            <button
              type="button"
              aria-label={expanded ? "Hide TXT challenge" : "Show TXT challenge"}
              aria-expanded={expanded}
              onClick={() => setExpanded((v) => !v)}
              className="inline-flex size-6 items-center justify-center rounded-md text-[var(--color-fg-subtle)] hover:bg-[var(--color-surface-sunken)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)] focus-visible:ring-offset-1"
            >
              {expanded ? (
                <ChevronDown className="size-4" />
              ) : (
                <ChevronRight className="size-4" />
              )}
            </button>
          ) : null}
        </TableCell>
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
      {canExpand && expanded ? (
        <TxtChallengeRow
          d={d}
          lastCheck={lastCheck}
          checking={verify.isPending}
          onCheckDNS={() => void handleCheckDNS()}
        />
      ) : null}
    </>
  );
}

// TxtChallengeRow — the expanded panel revealed under an unverified domain.
// Renders as a full-width <tr> so the panel inherits the table's column
// alignment without forcing the parent into a non-table layout. The Beacon
// surface tokens match the register-domain dialog's ChallengeRow so the
// operator sees the same visual vocabulary in both surfaces.
function TxtChallengeRow({
  d,
  lastCheck,
  checking,
  onCheckDNS,
}: {
  d: DomainEntry;
  lastCheck: { verified: boolean; at: Date } | null;
  checking: boolean;
  onCheckDNS: () => void;
}): React.ReactElement {
  // BFF derives txt_record_name as `_registry-verify.<domain>` but older
  // deployments may serve the FE-API-027 v1 response which omitted the
  // field. We fall back to deriving it client-side so a stale BFF doesn't
  // leave the panel empty — same string, just computed in two places.
  const txtName = d.txt_record_name || `_registry-verify.${d.domain}`;
  const txtValue = d.verification_token || "";
  const hasValue = txtValue.length > 0;

  // Live countdown — re-renders every 10s so the operator sees the cursor
  // tick down without the table thrashing on every paint. The hook returns
  // null when the cursor is absent or already in the past so the surface
  // can hide the line cleanly.
  const countdown = useNextPollCountdown(d.next_poll_after);

  return (
    <TableRow className="bg-[var(--color-surface-sunken)] hover:bg-[var(--color-surface-sunken)]">
      <TableCell colSpan={5} className="px-4 py-4">
        <div className="space-y-4 rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
          <div className="text-xs text-[var(--color-fg-muted)]">
            Add this TXT record at your DNS provider, then click{" "}
            <span className="font-medium text-[var(--color-fg)]">Check DNS now</span>{" "}
            to short-circuit the worker poll.
          </div>

          <ChallengeField label="TXT record name" value={txtName} />
          {hasValue ? (
            <ChallengeField
              label="TXT record value"
              value={txtValue}
              hint="Paste this exact string as the TXT record's value."
            />
          ) : (
            <div className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3 py-2 text-xs leading-relaxed text-[var(--color-fg-muted)]">
              The control plane is older than this dashboard and doesn't
              re-surface the verification token on the list response. Delete
              and re-register the domain to mint a fresh token.
            </div>
          )}

          <div className="flex flex-wrap items-center gap-3">
            <Button
              variant="outline"
              size="sm"
              onClick={onCheckDNS}
              loading={checking}
              disabled={checking}
            >
              <RefreshCw className="size-3.5" />
              Check DNS now
            </Button>
            {lastCheck ? (
              <span
                className={
                  lastCheck.verified
                    ? "text-xs text-[var(--color-success)]"
                    : "text-xs text-[var(--color-fg-muted)]"
                }
              >
                {lastCheck.verified ? (
                  <>
                    <CheckCircle2
                      className="mr-1 inline size-3.5 align-text-bottom"
                      aria-hidden
                    />
                    Found! Verifying…
                  </>
                ) : (
                  <>
                    Still pending — checked at{" "}
                    {lastCheck.at.toLocaleTimeString(undefined, {
                      hour: "2-digit",
                      minute: "2-digit",
                      second: "2-digit",
                    })}
                  </>
                )}
              </span>
            ) : null}
          </div>

          {countdown ? (
            <div className="text-[11px] text-[var(--color-fg-subtle)]">
              Worker will re-check automatically in{" "}
              <span className="font-mono text-[var(--color-fg-muted)]">
                {countdown}
              </span>
              .
            </div>
          ) : null}
        </div>
      </TableCell>
    </TableRow>
  );
}

// useNextPollCountdown — small hook isolating the 10s tick from the row
// component so the countdown can re-render without forcing the row's
// mutation hooks to re-subscribe. Returns null when the cursor is missing
// or already in the past (helper itself returns the literal string "now"
// in that case; the hook collapses that to null so the call site can hide
// the line entirely rather than rendering "in now").
function useNextPollCountdown(iso: string | null | undefined): string | null {
  const [, setTick] = React.useState(0);
  React.useEffect(() => {
    if (!iso) return undefined;
    const id = window.setInterval(() => setTick((t) => t + 1), 10_000);
    return () => window.clearInterval(id);
  }, [iso]);
  if (!iso) return null;
  const v = formatCountdown(iso);
  if (v === "now" || v === "—") return null;
  return v;
}

function ChallengeField({
  label,
  value,
  hint,
}: {
  label: string;
  value: string;
  hint?: string;
}): React.ReactElement {
  return (
    <div>
      <div className="mb-1.5 text-[11px] font-medium uppercase tracking-[0.14em] text-[var(--color-fg-subtle)]">
        {label}
      </div>
      <div className="flex items-center gap-2 rounded-md border border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] px-3 py-2">
        <code className="min-w-0 flex-1 truncate font-mono text-xs text-[var(--color-fg)]">
          {value}
        </code>
        <CopyButton value={value} iconOnly />
      </div>
      {hint ? (
        <p className="mt-1.5 text-xs leading-relaxed text-[var(--color-fg-subtle)]">
          {hint}
        </p>
      ) : null}
    </div>
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

import * as React from "react";
import { ChevronDown, ChevronRight } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorState } from "@/components/ui/error-state";
import { CopyButton } from "@/components/ui/copy-button";
import {
  useDelivery,
  type DeliveryStatus,
  type WebhookDeliveryDetail,
} from "@/lib/api/webhooks";
import { formatAbsoluteDate, formatRelativeDate } from "@/lib/format";
import { cn } from "@/lib/utils";

// Beacon — DeliveryDetailDialog (FE-API-035).
//
// Opens when an operator clicks a row in DeliveriesPanel. Shows the same
// summary the panel already paints (status / event / attempts / errors)
// plus three collapsible payload sections with copy-buttons.
//
// `signature_header` and `response_body` are reserved on the wire but the
// dispatcher hasn't been patched to fill them yet — see status.md
// FE-API-035. When empty we render a muted "Not captured" placeholder so
// the operator knows it's a backend follow-up, not a bug here.

interface DeliveryDetailDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  endpointId: string | undefined;
  deliveryId: string | undefined;
}

const STATUS_BADGE: Record<
  DeliveryStatus,
  React.ComponentProps<typeof Badge>["tone"]
> = {
  delivered: "success",
  pending: "warning",
  failed: "warning",
  dead: "danger",
};

export function DeliveryDetailDialog({
  open,
  onOpenChange,
  endpointId,
  deliveryId,
}: DeliveryDetailDialogProps): React.ReactElement {
  const q = useDelivery(endpointId, deliveryId);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-[720px]">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            Delivery detail
            {q.data ? (
              <Badge tone={STATUS_BADGE[q.data.status]}>
                {q.data.status}
              </Badge>
            ) : null}
          </DialogTitle>
          <DialogDescription>
            {deliveryId ? (
              <code className="font-mono text-xs">{deliveryId}</code>
            ) : (
              "No delivery selected."
            )}
          </DialogDescription>
        </DialogHeader>

        {q.isError ? (
          <ErrorState
            title="Couldn't load delivery"
            description="The /webhooks/{id}/deliveries/{delivery_id} endpoint didn't answer. Retry, or check the BFF logs."
            onRetry={() => void q.refetch()}
          />
        ) : q.isLoading || !q.data ? (
          <Body>
            <Skeleton className="h-4 w-2/3" />
            <Skeleton className="h-3 w-1/2" />
            <Skeleton className="h-24 w-full" />
          </Body>
        ) : (
          <Body>
            <Summary detail={q.data} />
            <Section
              title="Payload"
              value={q.data.payload_json}
              language="json"
              defaultOpen
              emptyHint="Empty payload."
            />
            <Section
              title="Signature header"
              value={q.data.signature_header}
              language="text"
              emptyHint="Not captured · backend follow-up tracked in status.md FE-API-035."
            />
            <Section
              title="Response body"
              value={q.data.response_body}
              language="text"
              emptyHint="Not captured · backend follow-up tracked in status.md FE-API-035."
            />
          </Body>
        )}
      </DialogContent>
    </Dialog>
  );
}

function Body({ children }: { children: React.ReactNode }): React.ReactElement {
  // Cap the body height so an enormous payload doesn't push the dialog
  // taller than the viewport — internal scroll keeps the modal usable.
  return <div className="max-h-[70vh] overflow-y-auto pr-1 space-y-5">{children}</div>;
}

function Summary({
  detail,
}: {
  detail: WebhookDeliveryDetail;
}): React.ReactElement {
  return (
    <div className="grid grid-cols-2 gap-3 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3 py-3 text-xs">
      <Field label="Event type">
        <code className="font-mono text-[var(--color-fg)]">
          {detail.event_type}
        </code>
      </Field>
      <Field label="Attempts">
        <span className="font-mono tabular-nums text-[var(--color-fg)]">
          {detail.attempts}/{detail.max_attempts}
        </span>
      </Field>
      <Field label="Created">
        <span
          className="text-[var(--color-fg)]"
          title={formatAbsoluteDate(detail.created_at)}
        >
          {formatRelativeDate(detail.created_at)}
        </span>
      </Field>
      <Field label={detail.delivered_at ? "Delivered" : "Next attempt"}>
        {detail.delivered_at ? (
          <span
            className="text-[var(--color-fg)]"
            title={formatAbsoluteDate(detail.delivered_at)}
          >
            {formatRelativeDate(detail.delivered_at)}
          </span>
        ) : detail.next_attempt_at ? (
          <span
            className="text-[var(--color-fg)]"
            title={formatAbsoluteDate(detail.next_attempt_at)}
          >
            {formatRelativeDate(detail.next_attempt_at)}
          </span>
        ) : (
          <span className="text-[var(--color-fg-subtle)]">—</span>
        )}
      </Field>
      {detail.last_error ? (
        <div className="col-span-2">
          <div className="text-[10px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Last error
          </div>
          <pre className="mt-1 overflow-x-auto rounded border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/5 p-2 font-mono text-[11px] text-[var(--color-fg)]">
            {detail.last_error}
          </pre>
        </div>
      ) : null}
    </div>
  );
}

function Field({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}): React.ReactElement {
  return (
    <div>
      <div className="text-[10px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
        {label}
      </div>
      <div className="mt-0.5">{children}</div>
    </div>
  );
}

// Section — collapsible payload viewer. Stays closed by default for the
// optional fields so the dialog opens in a compact shape; the primary
// payload section can pass `defaultOpen` to flip that.
function Section({
  title,
  value,
  language,
  defaultOpen,
  emptyHint,
}: {
  title: string;
  value: string;
  language: "json" | "text";
  defaultOpen?: boolean;
  emptyHint: string;
}): React.ReactElement {
  const [open, setOpen] = React.useState(Boolean(defaultOpen));
  const isEmpty = !value || value.trim() === "";
  const pretty = React.useMemo(() => prettyPrint(value, language), [value, language]);
  return (
    <section className="overflow-hidden rounded-md border border-[var(--color-border)] bg-[var(--color-surface)]">
      <button
        type="button"
        onClick={() => setOpen((p) => !p)}
        className={cn(
          "flex w-full items-center justify-between gap-2 px-3 py-2 text-left",
          "hover:bg-[var(--color-surface-sunken)]",
        )}
        aria-expanded={open}
      >
        <span className="flex items-center gap-1.5">
          {open ? (
            <ChevronDown className="size-3.5 text-[var(--color-fg-muted)]" />
          ) : (
            <ChevronRight className="size-3.5 text-[var(--color-fg-muted)]" />
          )}
          <span className="text-xs font-medium text-[var(--color-fg)]">
            {title}
          </span>
        </span>
        {!isEmpty && open ? (
          // Stop propagation so a copy-click doesn't collapse the section.
          <span onClick={(e) => e.stopPropagation()}>
            <CopyButton value={value} iconOnly />
          </span>
        ) : null}
      </button>
      {open ? (
        <div className="border-t border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3 py-2">
          {isEmpty ? (
            <p className="text-xs italic text-[var(--color-fg-subtle)]">
              {emptyHint}
            </p>
          ) : (
            <pre className="max-h-[320px] overflow-auto whitespace-pre-wrap break-all font-mono text-[11px] leading-relaxed text-[var(--color-fg)]">
              {pretty}
            </pre>
          )}
        </div>
      ) : null}
    </section>
  );
}

// prettyPrint — JSON.parse + re-stringify with 2-space indent for the
// payload section so an inlined JSON blob renders readably. Falls back
// to the original string when the value isn't valid JSON (or when the
// caller asked for "text" mode).
function prettyPrint(value: string, language: "json" | "text"): string {
  if (!value) return "";
  if (language !== "json") return value;
  try {
    return JSON.stringify(JSON.parse(value), null, 2);
  } catch {
    return value;
  }
}

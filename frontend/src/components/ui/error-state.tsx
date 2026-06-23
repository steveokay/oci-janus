import * as React from "react";
import { AlertTriangle } from "lucide-react";
import { Button } from "./button";
import { cn } from "@/lib/utils";
import { extractErrorMeta, type ErrorMeta } from "@/lib/api/error";

interface ErrorStateProps {
  title?: string;
  description?: string;
  // error optionally carries the underlying query / mutation error so the
  // state can render the HTTP status, server-side error message, request
  // URL and X-Request-ID. Designed for self-hosters: the operator viewing
  // this banner is the same person tailing the BFF logs (DSGN-004).
  error?: unknown;
  onRetry?: () => void;
  className?: string;
}

// Beacon — ErrorState. Inline (never modal) per the design direction.
// The "you can retry" CTA is the load-bearing piece; the title is for
// context. When an `error` is passed, the HTTP status pill renders next
// to the title and a "Show request details" expander gives the operator
// the server message + request URL + correlation id.
export function ErrorState({
  title = "Something went wrong",
  description = "We couldn't load this. Try again — if it keeps failing, the service may be unreachable.",
  error,
  onRetry,
  className,
}: ErrorStateProps): React.ReactElement {
  const meta: ErrorMeta = error ? extractErrorMeta(error) : {};
  const showCode = typeof meta.code === "number" && meta.code >= 400;
  const showExpander = Boolean(
    meta.detail || meta.requestId || meta.requestUrl,
  );

  return (
    <div
      role="alert"
      className={cn(
        "flex flex-col items-start gap-3 rounded-lg border border-[var(--color-danger)]/30",
        "bg-[var(--color-danger)]/5 px-5 py-4",
        className,
      )}
    >
      <div className="flex items-center gap-2 text-[var(--color-danger)]">
        <AlertTriangle className="size-4" aria-hidden />
        <span className="text-sm font-semibold">{title}</span>
        {showCode ? (
          <span
            className="rounded bg-[var(--color-danger)]/15 px-1.5 py-0.5 font-mono text-[10px] font-semibold uppercase tracking-wider"
            aria-label={`HTTP status ${meta.code}`}
          >
            HTTP {meta.code}
          </span>
        ) : null}
      </div>
      <p className="text-sm text-[var(--color-fg-muted)]">{description}</p>

      {showExpander ? (
        <details className="group w-full text-xs">
          <summary
            className={cn(
              "cursor-pointer select-none rounded text-[var(--color-fg-muted)] outline-none transition-colors",
              "hover:text-[var(--color-fg)]",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--color-bg)]",
            )}
          >
            Show request details
          </summary>
          <dl className="mt-2 space-y-1 rounded-md bg-[var(--color-surface-sunken)]/60 px-3 py-2 font-mono text-[11px] text-[var(--color-fg-muted)]">
            {meta.detail ? (
              <Row label="message" value={meta.detail} />
            ) : null}
            {meta.requestUrl ? (
              <Row label="url" value={meta.requestUrl} />
            ) : null}
            {meta.requestId ? (
              <Row label="x-request-id" value={meta.requestId} copyable />
            ) : null}
          </dl>
        </details>
      ) : null}

      {onRetry ? (
        <Button variant="outline" size="sm" onClick={onRetry}>
          Retry
        </Button>
      ) : null}
    </div>
  );
}

interface RowProps {
  label: string;
  value: string;
  copyable?: boolean;
}

// Row renders a single detail-grid line. `copyable` shows a click-to-copy
// affordance — useful for the request-id since it's the field the
// operator pastes into `grep` over BFF logs.
function Row({ label, value, copyable = false }: RowProps): React.ReactElement {
  const [copied, setCopied] = React.useState(false);
  React.useEffect(() => {
    if (!copied) return;
    const t = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(t);
  }, [copied]);

  async function onCopy(): Promise<void> {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
    } catch {
      // Clipboard API unavailable (no HTTPS / no permission); silently
      // swallow — the value is still selectable as text.
    }
  }

  return (
    <div className="flex items-baseline gap-2">
      <dt className="shrink-0 text-[var(--color-fg-subtle)]">{label}</dt>
      <dd className="min-w-0 flex-1 break-all text-[var(--color-fg)]">
        {value}
      </dd>
      {copyable ? (
        <button
          type="button"
          onClick={() => void onCopy()}
          className={cn(
            "shrink-0 rounded px-1.5 py-0.5 text-[10px] text-[var(--color-accent)]",
            "hover:bg-[var(--color-accent-subtle)]",
            "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)] focus-visible:ring-offset-1 focus-visible:ring-offset-[var(--color-surface-sunken)]",
          )}
          aria-label={`Copy ${label}`}
        >
          {copied ? "copied" : "copy"}
        </button>
      ) : null}
    </div>
  );
}

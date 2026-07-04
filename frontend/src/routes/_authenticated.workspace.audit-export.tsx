import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { toast } from "sonner";
import { AxiosError } from "axios";
import {
  Activity,
  AlertTriangle,
  CheckCircle2,
  KeyRound,
  Send,
  Trash2,
} from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorState } from "@/components/ui/error-state";
import { ConfirmDestructiveDialog } from "@/components/ui/confirm-destructive-dialog";
import {
  useAuditExportConfig,
  useUpdateAuditExportConfig,
  useDeleteAuditExportConfig,
  useTestAuditExportConfig,
  useDrainAuditExportDLX,
} from "@/lib/api/audit-export";
import type { AuditExportFormat, AuditExportTestResponse } from "@/lib/api/types";
import { formatRelativeDate, formatAbsoluteDate } from "@/lib/format";

// /workspace/audit-export — futures.md Tier 1 #4.
//
// One-screen editor for the tenant's SIEM streaming destination.
// Three formats: syslog (TCP/TLS), CEF (also syslog-framed), HTTPS
// webhook with HMAC-SHA256 or Bearer auth. Empty / disabled config
// means "no streaming" — the default for new tenants.
//
// Posture:
//   • Workspace-admin gate is enforced server-side; 403 surfaces as
//     a friendly toast rather than a destructive UI lock so a non-
//     admin reader still sees the current state for diagnostic value.
//   • Secrets are write-only: the GET response carries `*_set`
//     booleans (true means "we have something sealed in the DB").
//     The form renders "(saved)" placeholders + a "Replace" toggle
//     so rotating one secret doesn't accidentally clear the other.
//   • Last-success + DLX-depth surfaced as observability pills at
//     the top of the page so operators see at a glance whether the
//     stream is healthy.

export const Route = createFileRoute("/_authenticated/workspace/audit-export")({
  component: AuditExportPage,
});

const URL_PATTERNS: Record<AuditExportFormat, RegExp> = {
  // syslog+tcp:// or syslog+tls:// followed by host[:port]; we don't
  // re-validate the host shape here because the server applies the
  // SSRF guard at write time anyway.
  syslog_rfc5424: /^syslog\+(tcp|tls):\/\/.+/i,
  cef: /^syslog\+(tcp|tls):\/\/.+/i,
  // HTTPS only — plus localhost escape hatch for dev.
  webhook: /^(https:\/\/.+|http:\/\/(localhost|127\.0\.0\.1)(:|\/|$).*)$/i,
};

const FORMAT_OPTIONS: Array<{ value: AuditExportFormat; label: string; hint: string }> = [
  {
    value: "syslog_rfc5424",
    label: "Syslog (RFC 5424)",
    hint: "Structured-text over TCP/TLS. Splunk, QRadar, Elastic, Sumo accept this.",
  },
  {
    value: "cef",
    label: "CEF (ArcSight)",
    hint: "Common Event Format over syslog framing. ArcSight, Splunk, legacy SIEMs.",
  },
  {
    value: "webhook",
    label: "HTTPS webhook",
    hint: "JSON POST with X-Signature (HMAC-SHA256) or Authorization: Bearer.",
  },
];

const schema = z
  .object({
    enabled: z.boolean(),
    format: z.enum(["syslog_rfc5424", "cef", "webhook"]),
    target_url: z.string().trim().min(1, "Target URL is required."),
    hmac_secret: z.string().optional(),
    hmac_secret_clear: z.boolean().optional(),
    bearer_token: z.string().optional(),
    bearer_token_clear: z.boolean().optional(),
    event_filters_json: z.string().optional(),
  })
  .superRefine((val, ctx) => {
    const pattern = URL_PATTERNS[val.format];
    if (!pattern.test(val.target_url)) {
      ctx.addIssue({
        path: ["target_url"],
        code: z.ZodIssueCode.custom,
        message:
          val.format === "webhook"
            ? "Webhook URLs must be https:// (or http://localhost for dev)."
            : "Syslog URLs must start with syslog+tcp:// or syslog+tls://.",
      });
    }
    if (val.event_filters_json && val.event_filters_json.trim()) {
      try {
        JSON.parse(val.event_filters_json);
      } catch {
        ctx.addIssue({
          path: ["event_filters_json"],
          code: z.ZodIssueCode.custom,
          message: "Must be a valid JSON object.",
        });
      }
    }
  });
type FormValues = z.infer<typeof schema>;

function AuditExportPage(): React.ReactElement {
  const cfg = useAuditExportConfig();
  const update = useUpdateAuditExportConfig();
  const remove = useDeleteAuditExportConfig();
  const test = useTestAuditExportConfig();
  const drain = useDrainAuditExportDLX();
  const [lastTest, setLastTest] = React.useState<AuditExportTestResponse | null>(null);
  const [clearOpen, setClearOpen] = React.useState(false);

  // Hook form is re-initialised every time the GET resolves so the
  // form state stays in sync with the server-side row. defaultValues
  // are deliberately sensible-blank for the "no config yet" case.
  const form = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: {
      enabled: true,
      format: "webhook",
      target_url: "",
      hmac_secret: "",
      hmac_secret_clear: false,
      bearer_token: "",
      bearer_token_clear: false,
      event_filters_json: "",
    },
  });

  React.useEffect(() => {
    if (cfg.data) {
      form.reset({
        enabled: cfg.data.enabled,
        format: cfg.data.format,
        target_url: cfg.data.target_url,
        hmac_secret: "",
        hmac_secret_clear: false,
        bearer_token: "",
        bearer_token_clear: false,
        event_filters_json: cfg.data.event_filters_json ?? "",
      });
    }
  }, [cfg.data, form]);

  if (cfg.isError) {
    return (
      <ErrorState
        title="Couldn't load audit export config"
        description="The management API didn't answer. Retry, or check the BFF logs."
        error={cfg.error}
        onRetry={() => void cfg.refetch()}
      />
    );
  }

  const currentFormat = form.watch("format");
  const showWebhookSecrets = currentFormat === "webhook";

  async function onSubmit(values: FormValues): Promise<void> {
    try {
      await update.mutateAsync({
        enabled: values.enabled,
        format: values.format,
        target_url: values.target_url.trim(),
        hmac_secret: values.hmac_secret?.trim() || undefined,
        hmac_secret_clear: values.hmac_secret_clear,
        bearer_token: values.bearer_token?.trim() || undefined,
        bearer_token_clear: values.bearer_token_clear,
        event_filters_json: values.event_filters_json?.trim() || undefined,
      });
      toast.success("Audit export config saved.");
      // Reset the secret inputs so a follow-up save doesn't accidentally
      // re-send the same plaintext.
      form.setValue("hmac_secret", "");
      form.setValue("bearer_token", "");
      form.setValue("hmac_secret_clear", false);
      form.setValue("bearer_token_clear", false);
    } catch (e) {
      const ax = e as AxiosError<{ error?: string }> | undefined;
      const code = ax?.response?.status;
      const msg =
        code === 403
          ? "Workspace admin role required."
          : code === 412
            ? "Audit service secrets key isn't configured. See docs/SIEM-EXPORT.md."
            : (ax?.response?.data?.error ?? "Couldn't save audit export config.");
      toast.error(msg);
    }
  }

  async function onDelete(): Promise<void> {
    try {
      await remove.mutateAsync();
      toast.success("Audit export config cleared.");
      setLastTest(null);
      setClearOpen(false);
    } catch (e) {
      const code = (e as AxiosError | undefined)?.response?.status;
      toast.error(code === 403 ? "Workspace admin role required." : "Couldn't clear config.");
    }
  }

  async function onTest(): Promise<void> {
    try {
      const result = await test.mutateAsync();
      setLastTest(result);
      if (result.delivered) {
        toast.success("Test event delivered.");
      } else {
        toast.error(`Test failed: ${result.error ?? "unknown error"}`);
      }
    } catch (e) {
      const code = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        code === 404
          ? "Save the config first, then send a test."
          : code === 503
            ? "Audit export tester not wired on the audit service."
            : "Couldn't send test event.",
      );
    }
  }

  return (
    <div className="space-y-6">
      <header>
        {/* Aligned to the dominant page-title treatment used across routes
            (font-display text-3xl font-medium tracking-tight). */}
        <h1 className="font-display text-3xl font-medium tracking-tight">
          Audit log streaming
        </h1>
        <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
          Ship every audit event for this workspace to your SIEM. Three
          formats: syslog (RFC 5424), CEF, or HTTPS webhook. Disable at
          any time — clearing the config stops the stream on the next
          event.
        </p>
      </header>

      {cfg.isLoading ? (
        <Skeleton className="h-24 w-full" />
      ) : cfg.data ? (
        <ObservabilityCard
          cfg={cfg.data}
          onDrain={async () => {
            try {
              const r = await drain.mutateAsync();
              toast.success(
                r.republished === 0
                  ? "DLX is empty — nothing to drain."
                  : `Drained ${r.republished} parked event${r.republished === 1 ? "" : "s"}.`,
              );
            } catch (e) {
              const code = (e as AxiosError | undefined)?.response?.status;
              toast.error(
                code === 503
                  ? "DLX probe not wired on the audit service."
                  : "Couldn't drain DLX. Check the audit logs.",
              );
            }
          }}
          draining={drain.isPending}
        />
      ) : null}

      <Card>
        <CardHeader>
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Destination
          </CardDescription>
          <p className="mt-1 text-xs text-[var(--color-fg-muted)]">
            Choose a format + target. Secrets are encrypted at rest with
            AES-256-GCM and never returned over the wire.
          </p>
        </CardHeader>
        <CardContent>
          <form
            onSubmit={form.handleSubmit(onSubmit)}
            className="space-y-5"
          >
            <div className="space-y-1.5">
              <Label htmlFor="enabled">Enabled</Label>
              <div className="flex items-center justify-between rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3 py-2">
                <span className="text-sm text-[var(--color-fg)]">
                  When off, audit events stay in the local DB only.
                </span>
                <Switch
                  checked={form.watch("enabled")}
                  onCheckedChange={(v) => form.setValue("enabled", v)}
                  aria-label="Enabled"
                />
              </div>
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="format">Format</Label>
              <div className="grid gap-2 sm:grid-cols-3">
                {FORMAT_OPTIONS.map((opt) => (
                  <label
                    key={opt.value}
                    className={`cursor-pointer rounded-md border px-3 py-2 text-xs ${
                      form.watch("format") === opt.value
                        ? "border-[var(--color-accent)] bg-[var(--color-accent-subtle)]"
                        : "border-[var(--color-border)] bg-[var(--color-surface-sunken)]"
                    }`}
                  >
                    <input
                      type="radio"
                      value={opt.value}
                      checked={form.watch("format") === opt.value}
                      onChange={() => form.setValue("format", opt.value)}
                      className="sr-only"
                    />
                    <div className="font-medium text-[var(--color-fg)]">
                      {opt.label}
                    </div>
                    <div className="mt-0.5 text-[10.5px] text-[var(--color-fg-muted)]">
                      {opt.hint}
                    </div>
                  </label>
                ))}
              </div>
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="target_url">Target URL</Label>
              <Input
                id="target_url"
                autoComplete="off"
                placeholder={
                  currentFormat === "webhook"
                    ? "https://siem.example.com/audit"
                    : "syslog+tls://siem.example.com:6514"
                }
                {...form.register("target_url")}
              />
              {form.formState.errors.target_url ? (
                <p className="text-[11px] text-[var(--color-danger)]">
                  {form.formState.errors.target_url.message}
                </p>
              ) : null}
            </div>

            {showWebhookSecrets ? (
              <div className="space-y-4 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] p-3">
                <div className="space-y-1.5">
                  <Label htmlFor="hmac_secret">
                    <span className="inline-flex items-center gap-2">
                      <KeyRound className="size-3.5" />
                      HMAC shared secret
                      {cfg.data?.hmac_secret_set ? (
                        <Badge tone="success" className="!py-0 text-[10px]">
                          (saved)
                        </Badge>
                      ) : null}
                    </span>
                  </Label>
                  <Input
                    id="hmac_secret"
                    type="password"
                    autoComplete="off"
                    placeholder={
                      cfg.data?.hmac_secret_set
                        ? "Enter a new value to rotate, or leave blank"
                        : "Set a shared secret for X-Signature: sha256=…"
                    }
                    {...form.register("hmac_secret")}
                  />
                  {cfg.data?.hmac_secret_set ? (
                    <label className="flex items-center gap-2 text-[11px] text-[var(--color-fg-muted)]">
                      <input
                        type="checkbox"
                        {...form.register("hmac_secret_clear")}
                      />
                      Revoke the existing HMAC secret on save
                    </label>
                  ) : null}
                </div>

                <div className="space-y-1.5">
                  <Label htmlFor="bearer_token">
                    <span className="inline-flex items-center gap-2">
                      Bearer token (alternative)
                      {cfg.data?.bearer_token_set ? (
                        <Badge tone="success" className="!py-0 text-[10px]">
                          (saved)
                        </Badge>
                      ) : null}
                    </span>
                  </Label>
                  <Input
                    id="bearer_token"
                    type="password"
                    autoComplete="off"
                    placeholder={
                      cfg.data?.bearer_token_set
                        ? "Enter a new value to rotate, or leave blank"
                        : "Sent as Authorization: Bearer …"
                    }
                    {...form.register("bearer_token")}
                  />
                  {cfg.data?.bearer_token_set ? (
                    <label className="flex items-center gap-2 text-[11px] text-[var(--color-fg-muted)]">
                      <input
                        type="checkbox"
                        {...form.register("bearer_token_clear")}
                      />
                      Revoke the existing bearer token on save
                    </label>
                  ) : null}
                </div>

                <p className="text-[11px] text-[var(--color-fg-muted)]">
                  HMAC is the recommended path. Use bearer when your
                  collector only accepts a static Authorization header.
                  Set both to send HMAC (HMAC wins).
                </p>
              </div>
            ) : null}

            <div className="space-y-1.5">
              <Label htmlFor="event_filters_json">
                Event filters (optional JSON)
              </Label>
              <textarea
                id="event_filters_json"
                rows={4}
                className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3 py-2 font-mono text-[12px] text-[var(--color-fg)]"
                placeholder='{"include": ["push.completed", "scan.completed"], "exclude": ["webhook.*"]}'
                {...form.register("event_filters_json")}
              />
              <p className="text-[11px] text-[var(--color-fg-muted)]">
                Leave blank to ship every event. Wildcards: trailing{" "}
                <code className="font-mono text-[10px]">.*</code> matches
                any suffix. <code className="font-mono text-[10px]">exclude</code>{" "}
                wins over <code className="font-mono text-[10px]">include</code>.
              </p>
              {form.formState.errors.event_filters_json ? (
                <p className="text-[11px] text-[var(--color-danger)]">
                  {form.formState.errors.event_filters_json.message}
                </p>
              ) : null}
            </div>

            <div className="flex flex-wrap items-center justify-between gap-2 pt-2">
              <div className="flex gap-2">
                <Button type="submit" disabled={update.isPending}>
                  {update.isPending ? "Saving…" : cfg.data ? "Save changes" : "Save config"}
                </Button>
                {cfg.data ? (
                  <Button
                    type="button"
                    variant="outline"
                    disabled={test.isPending}
                    onClick={() => void onTest()}
                  >
                    <Send className="size-3.5" />{" "}
                    {test.isPending ? "Sending…" : "Send test event"}
                  </Button>
                ) : null}
              </div>
              {cfg.data ? (
                <Button
                  type="button"
                  variant="ghost"
                  className="text-[var(--color-danger)]"
                  disabled={remove.isPending}
                  onClick={() => setClearOpen(true)}
                >
                  <Trash2 className="size-3.5" /> Clear config
                </Button>
              ) : null}
            </div>
          </form>
        </CardContent>
      </Card>

      {lastTest ? <TestResultCard result={lastTest} /> : null}

      <ConfirmDestructiveDialog
        open={clearOpen}
        onOpenChange={setClearOpen}
        title="Clear audit export config?"
        description="Streaming will stop on the next event. Saved secrets are deleted from the database; you'll need to re-enter them to re-enable export."
        severity="low"
        confirmLabel="Clear config"
        loading={remove.isPending}
        onConfirm={onDelete}
      />
    </div>
  );
}

interface ObservabilityCardProps {
  cfg: import("@/lib/api/types").AuditExportConfig;
  onDrain: () => Promise<void>;
  draining: boolean;
}

function ObservabilityCard({ cfg, onDrain, draining }: ObservabilityCardProps): React.ReactElement {
  // dlx_queue_depth is the LIVE count from the RabbitMQ Mgmt API.
  // dlx_depth is the cumulative monotonic counter. Both are surfaced
  // but the actionable signal is the live one — drain only does
  // anything when dlx_queue_depth > 0.
  const liveQueued = cfg.dlx_queue_depth;
  const queueUnknown = liveQueued < 0;
  const stuckInQueue = liveQueued > 0;
  const healthy = !!cfg.last_success_at && !stuckInQueue && !cfg.last_error;
  return (
    <Card accentBar={healthy ? "accent" : "danger"}>
      <CardContent className="flex flex-wrap items-center gap-4 py-3">
        <div className="flex items-center gap-2">
          <Activity className="size-4 text-[var(--color-fg-muted)]" />
          <span className="text-[11px] uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Stream health
          </span>
        </div>
        {cfg.enabled ? (
          <Badge tone={healthy ? "success" : "warning"}>
            {healthy ? "Healthy" : "Degraded"}
          </Badge>
        ) : (
          <Badge tone="neutral">Disabled</Badge>
        )}
        <span className="text-xs text-[var(--color-fg-muted)]">
          Last success:{" "}
          {/* Relative form for scannability; absolute timestamp on hover.
              Matches the app-wide formatRelativeDate/formatAbsoluteDate
              timestamp convention. */}
          <span
            className="text-[var(--color-fg)]"
            title={
              cfg.last_success_at
                ? formatAbsoluteDate(cfg.last_success_at)
                : undefined
            }
          >
            {cfg.last_success_at ? formatRelativeDate(cfg.last_success_at) : "never"}
          </span>
        </span>
        {stuckInQueue ? (
          <span className="inline-flex items-center gap-1.5 text-xs text-[var(--color-warning)]">
            <AlertTriangle className="size-3.5" />
            <strong>{liveQueued}</strong> event{liveQueued === 1 ? "" : "s"} parked in DLX
          </span>
        ) : queueUnknown ? (
          <span className="text-xs text-[var(--color-fg-muted)]">
            DLX depth unavailable (RabbitMQ Mgmt API unreachable)
          </span>
        ) : (
          <span className="text-xs text-[var(--color-fg-muted)]">
            DLX empty
          </span>
        )}
        {cfg.dlx_depth > 0 ? (
          <span
            className="text-[11px] text-[var(--color-fg-subtle)]"
            title="Cumulative count of events that have ever landed in the DLX"
          >
            (lifetime parked: {cfg.dlx_depth})
          </span>
        ) : null}
        {cfg.last_error ? (
          <span
            className="truncate text-xs text-[var(--color-danger)]"
            title={cfg.last_error}
          >
            Last error: {cfg.last_error}
          </span>
        ) : null}
        {stuckInQueue ? (
          <Button
            size="sm"
            variant="outline"
            disabled={draining}
            onClick={() => void onDrain()}
            className="ml-auto"
          >
            {draining ? "Draining…" : "Drain DLX → retry"}
          </Button>
        ) : null}
      </CardContent>
    </Card>
  );
}

function TestResultCard({ result }: { result: AuditExportTestResponse }): React.ReactElement {
  return (
    <Card accentBar={result.delivered ? "accent" : "danger"}>
      <CardHeader className="pb-2">
        <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          Last test event
        </CardDescription>
        <div className="mt-1 flex items-center gap-2">
          {result.delivered ? (
            <Badge tone="success">
              <CheckCircle2 className="size-3" /> Delivered
            </Badge>
          ) : (
            <Badge tone="danger">
              <AlertTriangle className="size-3" /> Failed
            </Badge>
          )}
          {result.error ? (
            <span className="text-xs text-[var(--color-danger)]">{result.error}</span>
          ) : null}
        </div>
      </CardHeader>
      <CardContent>
        {result.rendered_event ? (
          <pre className="overflow-x-auto rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-4 py-3 font-mono text-[12px] text-[var(--color-fg)] whitespace-pre-wrap break-all">
            {result.rendered_event}
          </pre>
        ) : null}
      </CardContent>
    </Card>
  );
}

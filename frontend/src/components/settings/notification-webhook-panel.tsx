// FUT-019 Phase 3 — Notification webhook configuration panel.
//
// Renders below the email transport panel, above the Settings › Notifications
// category matrix. Lets a global admin set the org webhook endpoint + signing
// secret, enable the channel, send a test POST, and save the config. The
// signing secret is write-only: the GET never returns it, only a `has_secret`
// flag, so leaving the input blank keeps the stored secret (we send `""` to
// mean "unchanged").
//
// The webhook's per-category enablement (`enabled_categories`) is owned by the
// matrix Webhook column, NOT this panel — so save() preserves whatever the GET
// returned and never clobbers it.
//
// Admin-only: the panel renders nothing for non-admins. The BFF route is
// itself admin-gated (403 for non-admins), so the query would error — but we
// never mount for non-admins, so that path is unreachable here.
import * as React from "react";
import { toast } from "sonner";
import { Webhook } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useIsGlobalAdmin } from "@/lib/api/abilities";
import {
  useNotificationWebhook,
  useUpdateNotificationWebhook,
  useSendTestNotificationWebhook,
  type NotificationWebhookConfig,
  type NotificationWebhookPut,
} from "@/lib/api/notification-webhook";

// The card class matches the email-transport + notification-matrix section
// cards so the three stack as a visually consistent column.
const CARD_CLASS =
  "rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-5 shadow-[var(--shadow-card)]";

// FormState is the editable subset of the config plus the write-only secret
// input. The secret starts empty and only carries a value when the admin
// actively types one. enabled_categories is intentionally NOT in the form —
// the matrix owns it; save() reads it back off the fetched config.
interface FormState {
  url: string;
  enabled: boolean;
  secret: string;
}

// seedFrom builds the initial form state from a fetched config. The secret
// input is always blank on seed — the server never sends the secret back.
function seedFrom(cfg: NotificationWebhookConfig): FormState {
  return {
    url: cfg.url,
    enabled: cfg.enabled,
    secret: "",
  };
}

export function NotificationWebhookPanel(): React.ReactElement | null {
  const isAdmin = useIsGlobalAdmin();
  // Admin-only surface — render nothing for everyone else.
  if (!isAdmin) return null;
  return <NotificationWebhookPanelInner />;
}

// Inner component so the hooks below only run once we know the caller is an
// admin (the admin gate short-circuits before any of these fire).
function NotificationWebhookPanelInner(): React.ReactElement {
  const { data, isLoading, isError, refetch } = useNotificationWebhook();
  const update = useUpdateNotificationWebhook();
  const sendTest = useSendTestNotificationWebhook();

  const [form, setForm] = React.useState<FormState | null>(null);

  // Seed local form state once the config arrives. We only seed when we
  // don't already have a form so an in-flight edit isn't clobbered by a
  // background refetch.
  React.useEffect(() => {
    if (data && !form) setForm(seedFrom(data));
  }, [data, form]);

  if (isLoading || (!form && !isError)) {
    return (
      <section className={CARD_CLASS}>
        <PanelHeader />
        <p className="mt-3 text-sm text-[var(--color-fg-muted)]" role="status">
          Loading webhook config…
        </p>
      </section>
    );
  }

  if (isError || !form) {
    return (
      <section className={CARD_CLASS}>
        <PanelHeader />
        <p className="mt-3 text-sm text-[var(--color-danger)]">
          Couldn't load the webhook config.
        </p>
        <Button
          variant="outline"
          size="sm"
          className="mt-3"
          onClick={() => void refetch()}
        >
          Retry
        </Button>
      </section>
    );
  }

  // set updates a single field of the form immutably.
  function set<K extends keyof FormState>(key: K, value: FormState[K]): void {
    setForm((prev) => (prev ? { ...prev, [key]: value } : prev));
  }

  function save(): void {
    if (!form) return;
    const body: NotificationWebhookPut = {
      url: form.url,
      enabled: form.enabled,
      // Empty string = "keep the existing stored secret".
      secret: form.secret,
      // Preserve the matrix-owned per-category enablement — the panel must
      // not clobber what the Webhook column manages.
      enabled_categories: data?.enabled_categories ?? [],
    };
    update.mutate(body, {
      onSuccess: (next) => {
        // Re-seed from the server's canonical response (clears the secret
        // input + picks up the refreshed has_secret flag).
        setForm(seedFrom(next));
        toast.success("Webhook saved.");
      },
      onError: () => {
        toast.error("Couldn't save the webhook. Check the BFF logs.");
      },
    });
  }

  return (
    <section className={CARD_CLASS}>
      <PanelHeader />
      <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
        Configure the org webhook endpoint that receives notification events.
        The signing secret is write-only — leave it blank to keep the stored
        value.
      </p>

      <div className="mt-4 space-y-4">
        {/* Webhook URL */}
        <div>
          <Label htmlFor="webhook-url">Webhook URL</Label>
          <Input
            id="webhook-url"
            placeholder="https://hooks.example.com/..."
            value={form.url}
            onChange={(e) => set("url", e.target.value)}
          />
        </div>

        {/* Signing secret (write-only) */}
        <div>
          <Label htmlFor="webhook-secret">Signing secret</Label>
          <Input
            id="webhook-secret"
            type="password"
            autoComplete="off"
            placeholder={data?.has_secret ? "•••• configured" : ""}
            value={form.secret}
            onChange={(e) => set("secret", e.target.value)}
          />
        </div>

        {/* Enabled toggle */}
        <label className="flex items-center gap-2 text-sm text-[var(--color-fg)]">
          <input
            type="checkbox"
            checked={form.enabled}
            onChange={(e) => set("enabled", e.target.checked)}
            className="size-4 rounded border-[var(--color-border-strong)] accent-[var(--color-accent)] focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/40"
          />
          <span>Enabled</span>
        </label>

        {/* Test result banner — surfaces both the last stored test and the
            in-session mutation result. */}
        <TestResult data={data} result={sendTest.data} />

        {/* Actions */}
        <div className="flex flex-wrap items-center gap-2">
          <Button
            type="button"
            onClick={save}
            loading={update.isPending}
            disabled={update.isPending}
          >
            Save
          </Button>
          <Button
            type="button"
            variant="outline"
            onClick={() => sendTest.mutate()}
            loading={sendTest.isPending}
            disabled={sendTest.isPending}
          >
            Send test
          </Button>
        </div>
      </div>
    </section>
  );
}

function PanelHeader(): React.ReactElement {
  return (
    <div className="flex items-center gap-2">
      <Webhook className="size-4 text-[var(--color-fg-muted)]" />
      <h2 className="font-display text-lg font-medium">Notification webhook</h2>
    </div>
  );
}

// TestResult renders, in priority order, the in-session test-send outcome
// (from the mutation) and otherwise the last stored test result from the GET.
// Green when the send succeeded, red with the error message when it failed.
function TestResult({
  data,
  result,
}: {
  data: NotificationWebhookConfig | undefined;
  result: { ok: boolean; error: string } | undefined;
}): React.ReactElement | null {
  // Prefer the live mutation result if the admin just clicked "Send test".
  if (result) {
    return result.ok ? (
      <p className="text-sm text-[var(--color-success)]" role="status">
        Test webhook sent successfully.
      </p>
    ) : (
      <p className="text-sm text-[var(--color-danger)]" role="status">
        Test webhook failed: {result.error || "unknown error"}
      </p>
    );
  }

  // Otherwise surface the last stored test outcome from the config, if any.
  if (data?.last_test_at) {
    return data.last_test_ok ? (
      <p className="text-sm text-[var(--color-success)]">
        Last test succeeded ({data.last_test_at}).
      </p>
    ) : (
      <p className="text-sm text-[var(--color-danger)]">
        Last test failed: {data.last_test_error || "unknown error"} (
        {data.last_test_at}).
      </p>
    );
  }

  return null;
}

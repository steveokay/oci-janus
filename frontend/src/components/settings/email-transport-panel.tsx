// FUT-019 Phase 3 — Email transport configuration panel.
//
// Renders above the Settings › Notifications category matrix. Lets a global
// admin choose a delivery provider (Resend or SMTP/Gmail), set the from
// identity, enable the channel, send a test email to their own address, and
// save the config. Secret fields (Resend API key / SMTP password) are
// write-only: the GET never returns them, only `has_*` flags, so leaving an
// input blank keeps the stored secret (we send `""` to mean "unchanged").
//
// Admin-only: the panel renders nothing for non-admins. The BFF route is
// itself admin-gated (403 for non-admins), so the query would error — but we
// never mount for non-admins, so that path is unreachable here.
import * as React from "react";
import { toast } from "sonner";
import { Mail } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { PasswordInput } from "@/components/ui/password-input";
import { Label } from "@/components/ui/label";
import { useIsGlobalAdmin } from "@/lib/api/abilities";
import {
  useEmailTransport,
  useUpdateEmailTransport,
  useSendTestEmail,
  type EmailTransportConfig,
  type EmailTransportPut,
} from "@/lib/api/email-transport";

// The card class matches the notification-matrix section card so the two
// stack as a visually consistent pair.
const CARD_CLASS =
  "rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-5 shadow-[var(--shadow-card)]";

// Native <select> styling that mirrors the shared Input primitive so the
// two provider/TLS dropdowns match the text fields around them without
// pulling in the Radix Select surface (native is easier to drive in tests
// and the option set here is tiny + static).
const SELECT_CLASS =
  "flex h-10 w-full rounded-md border border-[var(--color-border-strong)] " +
  "bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-fg)] " +
  "focus-visible:outline-none focus-visible:border-[var(--color-accent)] " +
  "disabled:cursor-not-allowed disabled:opacity-50 transition-colors";

// FormState is the editable subset of the config plus the two write-only
// secret inputs. Secrets start empty and only carry a value when the admin
// actively types one.
interface FormState {
  provider: "resend" | "smtp";
  enabled: boolean;
  from_address: string;
  from_name: string;
  smtp_host: string;
  smtp_port: number;
  smtp_username: string;
  smtp_tls_mode: "starttls" | "implicit" | "none";
  resend_api_key: string;
  smtp_password: string;
}

// seedFrom builds the initial form state from a fetched config. Secret
// inputs are always blank on seed — the server never sends secrets back.
function seedFrom(cfg: EmailTransportConfig): FormState {
  return {
    provider: cfg.provider,
    enabled: cfg.enabled,
    from_address: cfg.from_address,
    from_name: cfg.from_name,
    smtp_host: cfg.smtp_host,
    smtp_port: cfg.smtp_port,
    smtp_username: cfg.smtp_username,
    smtp_tls_mode: cfg.smtp_tls_mode,
    resend_api_key: "",
    smtp_password: "",
  };
}

export function EmailTransportPanel(): React.ReactElement | null {
  const isAdmin = useIsGlobalAdmin();
  // Admin-only surface — render nothing for everyone else.
  if (!isAdmin) return null;
  return <EmailTransportPanelInner />;
}

// Inner component so the hooks below only run once we know the caller is an
// admin (the admin gate short-circuits before any of these fire).
function EmailTransportPanelInner(): React.ReactElement {
  const { data, isLoading, isError, refetch } = useEmailTransport();
  const update = useUpdateEmailTransport();
  const sendTest = useSendTestEmail();

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
          Loading email transport…
        </p>
      </section>
    );
  }

  if (isError || !form) {
    return (
      <section className={CARD_CLASS}>
        <PanelHeader />
        <p className="mt-3 text-sm text-[var(--color-danger)]">
          Couldn't load the email transport config.
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

  // useGmail applies the well-known Gmail SMTP submission preset in one click.
  function useGmail(): void {
    setForm((prev) =>
      prev
        ? {
            ...prev,
            provider: "smtp",
            smtp_host: "smtp.gmail.com",
            smtp_port: 587,
            smtp_tls_mode: "starttls",
          }
        : prev,
    );
  }

  function save(): void {
    if (!form) return;
    const body: EmailTransportPut = {
      provider: form.provider,
      enabled: form.enabled,
      from_address: form.from_address,
      from_name: form.from_name,
      smtp_host: form.smtp_host,
      smtp_port: form.smtp_port,
      smtp_username: form.smtp_username,
      smtp_tls_mode: form.smtp_tls_mode,
      // Empty string = "keep the existing stored secret".
      resend_api_key: form.resend_api_key,
      smtp_password: form.smtp_password,
    };
    update.mutate(body, {
      onSuccess: (next) => {
        // Re-seed from the server's canonical response (clears the secret
        // inputs + picks up refreshed has_* flags).
        setForm(seedFrom(next));
        toast.success("Email transport saved.");
      },
      onError: () => {
        toast.error("Couldn't save email transport. Check the BFF logs.");
      },
    });
  }

  return (
    <section className={CARD_CLASS}>
      <PanelHeader />
      <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
        Configure how the platform delivers email notifications. Secrets are
        write-only — leave a key or password field blank to keep the stored
        value.
      </p>

      <div className="mt-4 space-y-4">
        {/* Provider + Gmail preset */}
        <div className="flex flex-wrap items-end gap-3">
          <div className="min-w-[180px] flex-1">
            <Label htmlFor="email-provider">Provider</Label>
            <select
              id="email-provider"
              aria-label="Provider"
              className={SELECT_CLASS}
              value={form.provider}
              onChange={(e) =>
                set("provider", e.target.value as FormState["provider"])
              }
            >
              <option value="resend">Resend</option>
              <option value="smtp">SMTP</option>
            </select>
          </div>
          <Button type="button" variant="outline" onClick={useGmail}>
            Use Gmail
          </Button>
        </div>

        {/* Resend fields */}
        {form.provider === "resend" ? (
          <div>
            <Label htmlFor="resend-api-key">Resend API key</Label>
            <PasswordInput
              id="resend-api-key"
              autoComplete="off"
              placeholder={data?.has_resend_key ? "•••• configured" : ""}
              value={form.resend_api_key}
              onChange={(e) => set("resend_api_key", e.target.value)}
            />
          </div>
        ) : null}

        {/* SMTP fields */}
        {form.provider === "smtp" ? (
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <div className="sm:col-span-2">
              <Label htmlFor="smtp-host">SMTP host</Label>
              <Input
                id="smtp-host"
                value={form.smtp_host}
                onChange={(e) => set("smtp_host", e.target.value)}
              />
            </div>
            <div>
              <Label htmlFor="smtp-port">Port</Label>
              <Input
                id="smtp-port"
                type="number"
                value={String(form.smtp_port)}
                onChange={(e) =>
                  set("smtp_port", Number(e.target.value) || 0)
                }
              />
            </div>
            <div>
              <Label htmlFor="smtp-tls-mode">TLS mode</Label>
              <select
                id="smtp-tls-mode"
                aria-label="TLS mode"
                className={SELECT_CLASS}
                value={form.smtp_tls_mode}
                onChange={(e) =>
                  set(
                    "smtp_tls_mode",
                    e.target.value as FormState["smtp_tls_mode"],
                  )
                }
              >
                <option value="starttls">STARTTLS</option>
                <option value="implicit">Implicit</option>
                <option value="none">None</option>
              </select>
            </div>
            <div>
              <Label htmlFor="smtp-username">Username</Label>
              <Input
                id="smtp-username"
                autoComplete="off"
                value={form.smtp_username}
                onChange={(e) => set("smtp_username", e.target.value)}
              />
            </div>
            <div>
              <Label htmlFor="smtp-password">Password</Label>
              <PasswordInput
                id="smtp-password"
                autoComplete="off"
                placeholder={data?.has_smtp_password ? "•••• configured" : ""}
                value={form.smtp_password}
                onChange={(e) => set("smtp_password", e.target.value)}
              />
            </div>
          </div>
        ) : null}

        {/* From identity */}
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <div>
            <Label htmlFor="from-address">From address</Label>
            <Input
              id="from-address"
              type="email"
              placeholder="notifications@example.com"
              value={form.from_address}
              onChange={(e) => set("from_address", e.target.value)}
            />
          </div>
          <div>
            <Label htmlFor="from-name">From name</Label>
            <Input
              id="from-name"
              placeholder="Registry Notifications"
              value={form.from_name}
              onChange={(e) => set("from_name", e.target.value)}
            />
          </div>
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
            Send test email
          </Button>
        </div>
      </div>
    </section>
  );
}

function PanelHeader(): React.ReactElement {
  return (
    <div className="flex items-center gap-2">
      <Mail className="size-4 text-[var(--color-fg-muted)]" />
      <h2 className="font-display text-lg font-medium">Email transport</h2>
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
  data: EmailTransportConfig | undefined;
  result: { ok: boolean; error: string } | undefined;
}): React.ReactElement | null {
  // Prefer the live mutation result if the admin just clicked "Send test".
  if (result) {
    return result.ok ? (
      <p className="text-sm text-[var(--color-success)]" role="status">
        Test email sent successfully.
      </p>
    ) : (
      <p className="text-sm text-[var(--color-danger)]" role="status">
        Test email failed: {result.error || "unknown error"}
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

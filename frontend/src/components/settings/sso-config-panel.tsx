// FE-API-034 — SSO admin config panel.
//
// Global-admin-only editor for the deployment's OAuth SSO providers. Replaces
// the read-only SSOReadOnlyCard for admins (non-admins keep the read-only card
// at the mount sites). Renders the list of configured providers with edit +
// delete, and an add/edit form.
//
// Write-only client secret: the GET never returns the secret, only a
// `has_secret` flag. On PUT an empty client_secret keeps the stored value
// (mirrors the notification-webhook + pr-registry panels). On CREATE (a brand
// new id) the backend requires a non-empty secret and 400s otherwise.
//
// OAuth-only in v1: the kind picker offers Google / GitHub / Microsoft /
// Generic OIDC. The backend rejects `saml` with a 400 so we never offer it.
// `oauth_generic` requires an https issuer URL; named kinds don't.
//
// Admin-only: the panel renders nothing for non-admins. The BFF routes are
// themselves global-admin-gated (403 otherwise), so the query would error — but
// we never mount for non-admins, so that path is unreachable here.
import * as React from "react";
import { toast } from "sonner";
import { KeyRound, Plus, Pencil, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { PasswordInput } from "@/components/ui/password-input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { ConfirmDestructiveDialog } from "@/components/ui/confirm-destructive-dialog";
import { useIsGlobalAdmin } from "@/lib/api/abilities";
import {
  useSSOProviders,
  useUpsertSSOProvider,
  useDeleteSSOProvider,
  type AdminProvider,
  type PutBody,
} from "@/lib/api/sso-config";

// The card class matches the sibling settings panels so the column stacks
// consistently.
const CARD_CLASS =
  "rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-5 shadow-[var(--shadow-card)]";

// SSO_KINDS is the OAuth-only kind allowlist offered in the form. SAML is
// intentionally absent — the backend rejects it (400) in v1.
const SSO_KINDS = [
  { value: "oauth_google", label: "Google" },
  { value: "oauth_github", label: "GitHub" },
  { value: "oauth_microsoft", label: "Microsoft" },
  { value: "oauth_generic", label: "Generic OIDC" },
] as const;

// kindLabel maps a stored kind string to its human label (falls back to the
// raw value for any unexpected kind so we never render blank).
function kindLabel(kind: string): string {
  return SSO_KINDS.find((k) => k.value === kind)?.label ?? kind;
}

// FormState is the editable shape of a provider plus the write-only secret
// input and the id (only editable when creating). oauth_scopes is a free-text
// field (comma/space separated) parsed on save.
interface FormState {
  id: string;
  isNew: boolean;
  kind: string;
  display_name: string;
  oauth_client_id: string;
  oauth_issuer_url: string;
  scopesText: string;
  client_secret: string;
  enabled: boolean;
  auto_provision: boolean;
  // has_secret reflects the stored provider (drives the secret placeholder).
  // Always false for a brand-new provider.
  has_secret: boolean;
}

// emptyForm builds the initial state for the "add provider" flow.
function emptyForm(): FormState {
  return {
    id: "",
    isNew: true,
    kind: "oauth_google",
    display_name: "",
    oauth_client_id: "",
    oauth_issuer_url: "",
    scopesText: "",
    client_secret: "",
    enabled: true,
    auto_provision: false,
    has_secret: false,
  };
}

// formFrom builds edit-mode state from a fetched provider. The secret input is
// always blank on seed — the server never sends the secret back.
function formFrom(p: AdminProvider): FormState {
  return {
    id: p.id,
    isNew: false,
    kind: p.kind,
    display_name: p.display_name,
    oauth_client_id: p.oauth_client_id,
    oauth_issuer_url: p.oauth_issuer_url,
    scopesText: p.oauth_scopes.join(", "),
    client_secret: "",
    enabled: p.enabled,
    auto_provision: p.auto_provision,
    has_secret: p.has_secret,
  };
}

// parseScopes splits the free-text scopes field on commas and whitespace,
// trimming empties. "openid, email profile" → ["openid","email","profile"].
function parseScopes(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter((s) => s.length > 0);
}

export function SSOConfigPanel(): React.ReactElement | null {
  const isAdmin = useIsGlobalAdmin();
  // Admin-only surface — render nothing for everyone else.
  if (!isAdmin) return null;
  return <SSOConfigPanelInner />;
}

// Inner component so the hooks below only run once we know the caller is an
// admin (the admin gate short-circuits before any of these fire).
function SSOConfigPanelInner(): React.ReactElement {
  const { data: providers, isLoading, isError, refetch } = useSSOProviders();
  const upsert = useUpsertSSOProvider();
  const del = useDeleteSSOProvider();

  // The active edit/add form, or null when the form is closed.
  const [form, setForm] = React.useState<FormState | null>(null);
  // The provider queued for deletion (drives the confirm dialog), or null.
  const [pendingDelete, setPendingDelete] =
    React.useState<AdminProvider | null>(null);

  if (isLoading) {
    return (
      <section className={CARD_CLASS} id="sso">
        <PanelHeader />
        <p className="mt-3 text-sm text-[var(--color-fg-muted)]" role="status">
          Loading SSO providers…
        </p>
      </section>
    );
  }

  if (isError) {
    return (
      <section className={CARD_CLASS} id="sso">
        <PanelHeader />
        <p className="mt-3 text-sm text-[var(--color-danger)]">
          Couldn't load the SSO providers.
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

  const list = providers ?? [];

  // set updates a single field of the active form immutably.
  function set<K extends keyof FormState>(key: K, value: FormState[K]): void {
    setForm((prev) => (prev ? { ...prev, [key]: value } : prev));
  }

  function save(): void {
    if (!form) return;

    // Client-side guardrails that mirror the backend 400s — surfaced inline as
    // toasts so the admin doesn't round-trip for an obvious mistake.
    const id = form.id.trim();
    if (form.isNew && !/^[a-z][a-z0-9-]{1,63}$/.test(id)) {
      toast.error(
        "Provider id must be a lowercase slug (^[a-z][a-z0-9-]{1,63}$).",
      );
      return;
    }
    if (form.isNew && form.client_secret.trim() === "") {
      toast.error("A client secret is required when adding a provider.");
      return;
    }
    if (
      form.kind === "oauth_generic" &&
      !/^https:\/\//i.test(form.oauth_issuer_url.trim())
    ) {
      toast.error("Generic OIDC requires an https issuer URL.");
      return;
    }

    const body: PutBody = {
      kind: form.kind,
      display_name: form.display_name,
      enabled: form.enabled,
      oauth_client_id: form.oauth_client_id,
      // Named kinds don't need an issuer; send whatever was typed (empty for
      // named kinds, the https URL for generic).
      oauth_issuer_url: form.oauth_issuer_url,
      oauth_scopes: parseScopes(form.scopesText),
      // Empty string = "keep the existing stored secret" (existing providers).
      client_secret: form.client_secret,
      auto_provision: form.auto_provision,
    };

    upsert.mutate(
      { id, body },
      {
        onSuccess: () => {
          setForm(null);
          toast.success("SSO provider saved.");
        },
        onError: (err) => {
          const msg = extractError(err);
          toast.error(msg ?? "Couldn't save the SSO provider.");
        },
      },
    );
  }

  function confirmDelete(): void {
    if (!pendingDelete) return;
    const id = pendingDelete.id;
    del.mutate(id, {
      onSuccess: () => {
        setPendingDelete(null);
        // Close the edit form if it was editing the deleted provider.
        setForm((prev) => (prev && prev.id === id ? null : prev));
        toast.success("SSO provider deleted.");
      },
      onError: () => {
        setPendingDelete(null);
        toast.error("Couldn't delete the SSO provider.");
      },
    });
  }

  const isGeneric = form?.kind === "oauth_generic";

  return (
    <section className={CARD_CLASS} id="sso">
      <div className="flex items-center justify-between gap-2">
        <PanelHeader />
        {!form ? (
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={() => setForm(emptyForm())}
          >
            <Plus className="size-3.5" />
            Add provider
          </Button>
        ) : null}
      </div>

      <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
        Configure the OAuth SSO providers your users sign in with. Client
        secrets are write-only — leave the field blank when editing to keep the
        stored value.
      </p>

      {/* Configured providers list */}
      <div className="mt-4 space-y-2">
        {list.length === 0 ? (
          <p className="text-sm text-[var(--color-fg-subtle)]">
            No SSO providers configured yet.
          </p>
        ) : (
          list.map((p) => (
            <div
              key={p.id}
              className="flex items-center justify-between gap-3 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3 py-2"
            >
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <span className="truncate font-medium">
                    {p.display_name || p.id}
                  </span>
                  <Badge tone={p.enabled ? "success" : "neutral"}>
                    {p.enabled ? "Enabled" : "Disabled"}
                  </Badge>
                  {p.has_secret ? (
                    <Badge tone="accent">Secret set</Badge>
                  ) : (
                    <Badge tone="warning">No secret</Badge>
                  )}
                </div>
                <p className="mt-0.5 truncate text-xs text-[var(--color-fg-subtle)]">
                  {kindLabel(p.kind)} · {p.id}
                </p>
              </div>
              <div className="flex shrink-0 items-center gap-2">
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={() => setForm(formFrom(p))}
                >
                  <Pencil className="size-3.5" />
                  Edit
                </Button>
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={() => setPendingDelete(p)}
                >
                  <Trash2 className="size-3.5" />
                  Delete
                </Button>
              </div>
            </div>
          ))
        )}
      </div>

      {/* Add / edit form */}
      {form ? (
        <div className="mt-5 space-y-4 rounded-md border border-[var(--color-border)] p-4">
          <h3 className="font-display text-base font-medium">
            {form.isNew ? "Add SSO provider" : `Edit ${form.display_name || form.id}`}
          </h3>

          {/* Kind — native select so the option set is trivially testable and
              avoids OS-theme clash concerns (only 4 static options). */}
          <div>
            <Label htmlFor="sso-kind">Provider kind</Label>
            <select
              id="sso-kind"
              aria-label="Provider kind"
              className="mt-1 block h-9 w-full rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] px-2.5 text-sm text-[var(--color-fg)] focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]"
              value={form.kind}
              onChange={(e) => set("kind", e.target.value)}
            >
              {SSO_KINDS.map((k) => (
                <option key={k.value} value={k.value}>
                  {k.label}
                </option>
              ))}
            </select>
          </div>

          {/* Provider id — only editable when creating. For named kinds the
              natural id is google/github/microsoft; generic uses an admin slug. */}
          <div>
            <Label htmlFor="sso-id">Provider id</Label>
            <Input
              id="sso-id"
              placeholder="google"
              value={form.id}
              disabled={!form.isNew}
              onChange={(e) => set("id", e.target.value)}
            />
            <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">
              Lowercase slug. Fixed once created.
            </p>
          </div>

          {/* Display name */}
          <div>
            <Label htmlFor="sso-display-name">Display name</Label>
            <Input
              id="sso-display-name"
              placeholder="Sign in with Google"
              value={form.display_name}
              onChange={(e) => set("display_name", e.target.value)}
            />
          </div>

          {/* OAuth client id */}
          <div>
            <Label htmlFor="sso-client-id">OAuth client ID</Label>
            <Input
              id="sso-client-id"
              autoComplete="off"
              value={form.oauth_client_id}
              onChange={(e) => set("oauth_client_id", e.target.value)}
            />
          </div>

          {/* Client secret (write-only) */}
          <div>
            <Label htmlFor="sso-client-secret">OAuth client secret</Label>
            <PasswordInput
              id="sso-client-secret"
              autoComplete="off"
              placeholder={
                form.has_secret
                  ? "•••• configured, leave blank to keep"
                  : ""
              }
              value={form.client_secret}
              onChange={(e) => set("client_secret", e.target.value)}
            />
            <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">
              Write-only — sealed at rest and never displayed again.
              {form.isNew ? " Required when adding a provider." : ""}
            </p>
          </div>

          {/* Issuer URL — generic OIDC only */}
          {isGeneric ? (
            <div>
              <Label htmlFor="sso-issuer-url">Issuer URL</Label>
              <Input
                id="sso-issuer-url"
                placeholder="https://issuer.example.com"
                value={form.oauth_issuer_url}
                onChange={(e) => set("oauth_issuer_url", e.target.value)}
              />
              <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">
                Required for generic OIDC — must be https.
              </p>
            </div>
          ) : null}

          {/* OAuth scopes (optional) */}
          <div>
            <Label htmlFor="sso-scopes">
              OAuth scopes{" "}
              <span className="text-[var(--color-fg-subtle)]">(optional)</span>
            </Label>
            <Input
              id="sso-scopes"
              placeholder="openid, email, profile"
              value={form.scopesText}
              onChange={(e) => set("scopesText", e.target.value)}
            />
            <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">
              Comma or space separated.
            </p>
          </div>

          {/* Enabled toggle */}
          <label className="flex items-center gap-2 text-sm text-[var(--color-fg)]">
            <Switch
              checked={form.enabled}
              onCheckedChange={(v) => set("enabled", v)}
              aria-label="Enabled"
            />
            <span>Enabled</span>
          </label>

          {/* Auto-provision toggle */}
          <label className="flex items-center gap-2 text-sm text-[var(--color-fg)]">
            <Switch
              checked={form.auto_provision}
              onCheckedChange={(v) => set("auto_provision", v)}
              aria-label="Auto-provision users"
            />
            <span>Auto-provision users on first sign-in</span>
          </label>

          {/* Actions */}
          <div className="flex flex-wrap items-center gap-2">
            <Button
              type="button"
              onClick={save}
              loading={upsert.isPending}
              disabled={upsert.isPending}
            >
              Save
            </Button>
            <Button
              type="button"
              variant="outline"
              onClick={() => setForm(null)}
              disabled={upsert.isPending}
            >
              Cancel
            </Button>
          </div>
        </div>
      ) : null}

      {/* Delete confirmation */}
      <ConfirmDestructiveDialog
        open={pendingDelete !== null}
        onOpenChange={(open) => {
          if (!open) setPendingDelete(null);
        }}
        title="Delete SSO provider"
        description={
          <>
            Remove{" "}
            <span className="font-medium text-[var(--color-fg)]">
              {pendingDelete?.display_name || pendingDelete?.id}
            </span>
            ? Users will no longer be able to sign in with this provider.
          </>
        }
        severity="low"
        confirmLabel="Delete provider"
        onConfirm={confirmDelete}
        loading={del.isPending}
      />
    </section>
  );
}

// extractError pulls a human message out of the API error envelope
// { error: { code, message } } returned on 400s, falling back to null.
function extractError(err: unknown): string | null {
  const data = (err as { response?: { data?: { error?: { message?: string } } } })
    ?.response?.data;
  return data?.error?.message ?? null;
}

function PanelHeader(): React.ReactElement {
  return (
    <div className="flex items-center gap-2">
      <KeyRound className="size-4 text-[var(--color-fg-muted)]" />
      <h2 className="font-display text-lg font-medium">Single sign-on</h2>
    </div>
  );
}

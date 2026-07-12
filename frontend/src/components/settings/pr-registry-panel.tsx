// FUT-023 Phase 1 — ephemeral PR-scoped registries admin config panel.
//
// Lets a global admin enable the feature, copy the public GitHub-webhook
// receiver URL, set the write-only HMAC signing secret, and pick an optional
// promote-on-merge target org. Mirrors the notification-webhook panel: the
// signing secret is write-only (the GET only returns a `has_secret` flag, so a
// blank input keeps the stored secret — we send "" to mean "unchanged").
//
// Admin-only: renders nothing for non-admins. The BFF routes are themselves
// global-admin-gated (403 otherwise), so the query would error — but we never
// mount for non-admins, so that path is unreachable here.
//
// KEK dependency: enabling the feature seals the secret under
// PR_REGISTRY_KEY_HEX on the metadata side. If that KEK is unset the PUT fails
// with 409 — we surface that as a targeted toast telling the operator to
// configure the KEK first, rather than a generic "save failed".
import * as React from "react";
import { toast } from "sonner";
import { GitPullRequest } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { PasswordInput } from "@/components/ui/password-input";
import { Label } from "@/components/ui/label";
import { CopyButton } from "@/components/ui/copy-button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useIsGlobalAdmin } from "@/lib/api/abilities";
import { useRepositories } from "@/lib/api/repositories";
import {
  usePRRegistryConfig,
  useUpdatePRRegistryConfig,
  type PRRegistryConfig,
  type PRRegistryConfigPut,
} from "@/lib/api/pr-registry";

// The card class matches the sibling settings panels so the column stacks
// consistently.
const CARD_CLASS =
  "rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-5 shadow-[var(--shadow-card)]";

// NONE is the sentinel Select value for "no promote target". Radix Select
// items can't carry an empty-string value, so we map this ↔ "" at the edges.
const NONE = "__none__";

// FormState is the editable subset of the config plus the write-only secret
// input. The secret starts empty and only carries a value when the admin
// actively types one.
interface FormState {
  enabled: boolean;
  secret: string;
  promoteTargetOrg: string; // "" = no promote target
}

// seedFrom builds the initial form state from a fetched config. The secret
// input is always blank on seed — the server never sends the secret back.
function seedFrom(cfg: PRRegistryConfig): FormState {
  return {
    enabled: cfg.enabled,
    secret: "",
    promoteTargetOrg: cfg.promote_target_org,
  };
}

export function PRRegistryPanel(): React.ReactElement | null {
  const isAdmin = useIsGlobalAdmin();
  // Admin-only surface — render nothing for everyone else.
  if (!isAdmin) return null;
  return <PRRegistryPanelInner />;
}

// Inner component so the hooks below only run once we know the caller is an
// admin (the admin gate short-circuits before any of these fire).
function PRRegistryPanelInner(): React.ReactElement {
  const { data, isLoading, isError, refetch } = usePRRegistryConfig();
  const update = useUpdatePRRegistryConfig();

  const [form, setForm] = React.useState<FormState | null>(null);

  // Seed local form state once the config arrives. Only seed when we don't
  // already have a form so an in-flight edit isn't clobbered by a background
  // refetch.
  React.useEffect(() => {
    if (data && !form) setForm(seedFrom(data));
  }, [data, form]);

  if (isLoading || (!form && !isError)) {
    return (
      <section className={CARD_CLASS}>
        <PanelHeader />
        <p className="mt-3 text-sm text-[var(--color-fg-muted)]" role="status">
          Loading PR-registry config…
        </p>
      </section>
    );
  }

  if (isError || !form) {
    return (
      <section className={CARD_CLASS}>
        <PanelHeader />
        <p className="mt-3 text-sm text-[var(--color-danger)]">
          Couldn't load the PR-registry config.
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
    const body: PRRegistryConfigPut = {
      enabled: form.enabled,
      // Empty string = "keep the existing stored secret".
      webhook_secret: form.secret,
      promote_target_org: form.promoteTargetOrg,
    };
    update.mutate(body, {
      onSuccess: (next) => {
        // Re-seed from the server's canonical response (clears the secret
        // input + picks up the refreshed has_secret flag).
        setForm(seedFrom(next));
        toast.success("PR-registry config saved.");
      },
      onError: (err) => {
        // A 409 from the BFF means the metadata KEK (PR_REGISTRY_KEY_HEX) is
        // unset, so the secret can't be sealed. Surface the actionable message
        // rather than a generic failure.
        const status = (err as { response?: { status?: number } })?.response
          ?.status;
        if (status === 409) {
          toast.error(
            "Set PR_REGISTRY_KEY_HEX on registry-metadata before enabling PR registries.",
          );
        } else {
          toast.error("Couldn't save the PR-registry config. Check the BFF logs.");
        }
      },
    });
  }

  const webhookURL = data?.webhook_url ?? "";

  return (
    <section className={CARD_CLASS}>
      <PanelHeader />
      <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
        Auto-provision an ephemeral <code className="font-mono">pr-&lt;repo&gt;-&lt;N&gt;</code>{" "}
        org per GitHub pull request and tear it down on close. The signing
        secret is write-only — leave it blank to keep the stored value.
      </p>

      <div className="mt-4 space-y-4">
        {/* Webhook receiver URL (read-only, copyable) */}
        <div>
          <Label htmlFor="pr-registry-webhook-url">Webhook receiver URL</Label>
          <div className="flex items-center gap-2">
            <Input
              id="pr-registry-webhook-url"
              readOnly
              className="font-mono"
              placeholder="(set PUBLIC_BASE_URL on registry-management)"
              value={webhookURL}
            />
            {webhookURL ? (
              <CopyButton value={webhookURL} label="Copy" />
            ) : null}
          </div>
          <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">
            Add this as a GitHub webhook (content-type{" "}
            <code className="font-mono">application/json</code>, the{" "}
            <code className="font-mono">Pull requests</code> event) with the
            signing secret below.
          </p>
        </div>

        {/* Signing secret (write-only) */}
        <div>
          <Label htmlFor="pr-registry-secret">Signing secret</Label>
          <PasswordInput
            id="pr-registry-secret"
            autoComplete="off"
            placeholder={data?.has_secret ? "•••• configured" : ""}
            value={form.secret}
            onChange={(e) => set("secret", e.target.value)}
          />
          <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">
            HMAC-SHA256 secret GitHub signs each delivery with
            (X-Hub-Signature-256). Sealed at rest; never displayed again.
          </p>
        </div>

        {/* Promote-on-merge target org (optional) */}
        <PromoteTargetSelect
          value={form.promoteTargetOrg}
          onChange={(v) => set("promoteTargetOrg", v)}
        />

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
        </div>
      </div>
    </section>
  );
}

// PromoteTargetSelect renders the optional promote-on-merge destination org as
// a dropdown sourced from the caller's visible orgs (same source the promote
// dialog uses). A leading "None" option clears the target. When the currently
// stored value isn't among the visible orgs (e.g. an org with no repos yet), it
// is included so the saved value always displays.
function PromoteTargetSelect({
  value,
  onChange,
}: {
  value: string;
  onChange: (next: string) => void;
}): React.ReactElement {
  // Widest RBAC-aware org set the FE already has — every repo here passed the
  // BFF's reader gate on the caller. A global admin sees every org.
  const repos = useRepositories({ perPage: 100 });

  const orgOptions = React.useMemo<string[]>(() => {
    const set = new Set<string>();
    for (const page of repos.data?.pages ?? []) {
      for (const r of page.repositories) {
        if (r.org) set.add(r.org);
      }
    }
    // Always surface the currently-saved target even if no repo references it.
    if (value) set.add(value);
    return Array.from(set).sort();
  }, [repos.data, value]);

  return (
    <div>
      <Label htmlFor="pr-registry-promote-target" className="mb-2 inline-block">
        Promote-on-merge target org{" "}
        <span className="text-[var(--color-fg-subtle)]">(optional)</span>
      </Label>
      <Select
        value={value === "" ? NONE : value}
        onValueChange={(v) => onChange(v === NONE ? "" : v)}
      >
        <SelectTrigger
          id="pr-registry-promote-target"
          className="w-full font-mono"
        >
          <SelectValue placeholder="None" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={NONE}>None (don't promote)</SelectItem>
          {orgOptions.map((org) => (
            <SelectItem key={org} value={org} className="font-mono">
              {org}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">
        When a PR merges, its images are promoted into this org before the
        ephemeral namespace is torn down. Leave as None to just tear down.
      </p>
    </div>
  );
}

function PanelHeader(): React.ReactElement {
  return (
    <div className="flex items-center gap-2">
      <GitPullRequest className="size-4 text-[var(--color-fg-muted)]" />
      <h2 className="font-display text-lg font-medium">
        Ephemeral PR registries
      </h2>
    </div>
  );
}

import * as React from "react";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useServiceAccounts } from "@/lib/api/service-accounts";
import { useCreateOIDCTrust } from "@/lib/api/oidc-trust";
import { validateGlobSyntax } from "@/lib/oidc-subject-glob";

// CreateOIDCTrustDialog — new-trust modal for the FUT-001 TrustPanel.
//
// Fields:
//   display_name          — required, free text (operator-friendly label)
//   service_account_id    — required, picked from the tenant's SA list
//   issuer_url            — required, must start with https://
//   audience              — required, free text (usually the registry URL)
//   subject_pattern       — required, validated with validateGlobSyntax
//   jwks_cache_ttl_seconds — optional, default 3600
//
// All validation runs client-side before the POST — the mutation is only
// invoked when the form is clean. On mutation failure the server error
// message (or a generic fallback) surfaces in an inline banner. Uses a
// native <select> for the SA picker because Radix Select doesn't expose
// a Label association in a way that testing-library's getByLabelText
// can pick up without extra plumbing — the CreateServiceAccountDialog
// makes the same trade-off.

const DEFAULT_TTL_SECONDS = 3600;

interface CreateOIDCTrustDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // onCreated fires once the mutation resolves — the parent panel can
  // e.g. select the new row. Optional; TrustPanel doesn't need it.
  onCreated?: (id: string) => void;
}

// serverErrorMessage extracts a human-readable message from a thrown
// axios error. Falls back to a generic message if the shape doesn't
// match. Kept small — the BE returns { message: string } on 4xx.
function serverErrorMessage(err: unknown): string {
  if (err && typeof err === "object" && "response" in err) {
    const resp = (err as { response?: { data?: { message?: string } } })
      .response;
    const msg = resp?.data?.message;
    if (typeof msg === "string" && msg.length > 0) return msg;
  }
  return "Something went wrong creating the trust. Try again.";
}

export function CreateOIDCTrustDialog({
  open,
  onOpenChange,
  onCreated,
}: CreateOIDCTrustDialogProps): React.ReactElement {
  const serviceAccounts = useServiceAccounts();
  const createTrust = useCreateOIDCTrust();

  // Form state.
  const [displayName, setDisplayName] = React.useState("");
  const [serviceAccountId, setServiceAccountId] = React.useState("");
  const [issuerUrl, setIssuerUrl] = React.useState("");
  const [audience, setAudience] = React.useState("");
  const [subjectPattern, setSubjectPattern] = React.useState("");
  const [ttlSeconds, setTtlSeconds] =
    React.useState<number>(DEFAULT_TTL_SECONDS);

  // Per-field client-validation errors surfaced after submit. Cleared
  // when the field changes so operators can correct + retry inline.
  const [fieldErrors, setFieldErrors] = React.useState<{
    display_name?: string;
    service_account_id?: string;
    issuer_url?: string;
    audience?: string;
    subject_pattern?: string;
  }>({});

  // Default the SA picker to the first row once data lands.
  React.useEffect(() => {
    if (
      !serviceAccountId &&
      serviceAccounts.data &&
      serviceAccounts.data.length > 0
    ) {
      setServiceAccountId(serviceAccounts.data[0].id);
    }
  }, [serviceAccountId, serviceAccounts.data]);

  // Reset all state when the dialog closes so a re-open sequence starts
  // clean.
  React.useEffect(() => {
    if (!open) {
      setDisplayName("");
      setServiceAccountId("");
      setIssuerUrl("");
      setAudience("");
      setSubjectPattern("");
      setTtlSeconds(DEFAULT_TTL_SECONDS);
      setFieldErrors({});
    }
  }, [open]);

  // validateForm — collect every per-field error in one pass so the
  // operator sees the full picture, not a "fix one, submit, see the
  // next" trail.
  function validateForm(): typeof fieldErrors {
    const errs: typeof fieldErrors = {};
    if (displayName.trim().length === 0) {
      errs.display_name = "Display name is required.";
    }
    if (serviceAccountId.length === 0) {
      errs.service_account_id = "Choose a service account.";
    }
    if (issuerUrl.trim().length === 0) {
      errs.issuer_url = "Issuer URL is required.";
    } else if (!issuerUrl.startsWith("https://")) {
      errs.issuer_url = "Issuer URL must start with https://";
    }
    if (audience.trim().length === 0) {
      errs.audience = "Audience is required.";
    }
    if (subjectPattern.length === 0) {
      errs.subject_pattern = "Subject pattern is required.";
    } else {
      const globCheck = validateGlobSyntax(subjectPattern);
      if (!globCheck.ok) {
        errs.subject_pattern = globCheck.error;
      }
    }
    return errs;
  }

  async function handleSubmit(e: React.FormEvent): Promise<void> {
    e.preventDefault();
    const errs = validateForm();
    setFieldErrors(errs);
    if (Object.keys(errs).length > 0) return;

    try {
      const trust = await createTrust.mutateAsync({
        service_account_id: serviceAccountId,
        display_name: displayName.trim(),
        issuer_url: issuerUrl.trim(),
        audience: audience.trim(),
        subject_pattern: subjectPattern,
        jwks_cache_ttl_seconds: ttlSeconds,
      });
      onOpenChange(false);
      onCreated?.(trust.id);
    } catch {
      // Error message surfaces from createTrust.error below.
    }
  }

  const serverErr = createTrust.error
    ? serverErrorMessage(createTrust.error)
    : null;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>New federated trust</DialogTitle>
          <DialogDescription>
            Allow a CI provider to exchange its OIDC token for a short-lived
            registry token — no static API key required.
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={(e) => void handleSubmit(e)} className="space-y-5">
          {serverErr ? (
            <div
              role="alert"
              className="rounded-md border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/5 px-4 py-3 text-sm text-[var(--color-danger)]"
            >
              {serverErr}
            </div>
          ) : null}

          {/* Display name */}
          <div className="space-y-1.5">
            <Label htmlFor="trust-display-name">Display name *</Label>
            <Input
              id="trust-display-name"
              autoFocus
              placeholder="GHA prod"
              value={displayName}
              onChange={(e) => {
                setDisplayName(e.target.value);
                if (fieldErrors.display_name) {
                  setFieldErrors({ ...fieldErrors, display_name: undefined });
                }
              }}
              aria-invalid={!!fieldErrors.display_name}
              aria-describedby={
                fieldErrors.display_name ? "trust-display-name-error" : undefined
              }
            />
            {fieldErrors.display_name ? (
              <p
                id="trust-display-name-error"
                className="text-xs text-[var(--color-danger)]"
              >
                {fieldErrors.display_name}
              </p>
            ) : null}
          </div>

          {/* Service account — native <select> so getByLabelText works
              without extra Radix plumbing. */}
          <div className="space-y-1.5">
            <Label htmlFor="trust-service-account">Service account *</Label>
            <select
              id="trust-service-account"
              value={serviceAccountId}
              onChange={(e) => {
                setServiceAccountId(e.target.value);
                if (fieldErrors.service_account_id) {
                  setFieldErrors({
                    ...fieldErrors,
                    service_account_id: undefined,
                  });
                }
              }}
              className="w-full rounded-md border border-[var(--color-border-strong)] bg-[var(--color-surface)] px-3 py-2 text-sm"
              aria-invalid={!!fieldErrors.service_account_id}
              aria-describedby={
                fieldErrors.service_account_id
                  ? "trust-sa-error"
                  : undefined
              }
            >
              {(serviceAccounts.data ?? []).map((sa) => (
                <option key={sa.id} value={sa.id}>
                  {sa.name}
                  {sa.disabled_at ? " (disabled)" : ""}
                </option>
              ))}
            </select>
            {fieldErrors.service_account_id ? (
              <p
                id="trust-sa-error"
                className="text-xs text-[var(--color-danger)]"
              >
                {fieldErrors.service_account_id}
              </p>
            ) : (
              <p className="text-[11px] text-[var(--color-fg-subtle)]">
                Tokens exchanged through this trust are scoped by the picked
                service account.
              </p>
            )}
          </div>

          {/* Issuer URL */}
          <div className="space-y-1.5">
            <Label htmlFor="trust-issuer-url">Issuer URL *</Label>
            <Input
              id="trust-issuer-url"
              placeholder="https://token.actions.githubusercontent.com"
              value={issuerUrl}
              onChange={(e) => {
                setIssuerUrl(e.target.value);
                if (fieldErrors.issuer_url) {
                  setFieldErrors({ ...fieldErrors, issuer_url: undefined });
                }
              }}
              aria-invalid={!!fieldErrors.issuer_url}
              aria-describedby={
                fieldErrors.issuer_url ? "trust-issuer-error" : undefined
              }
            />
            {fieldErrors.issuer_url ? (
              <p
                id="trust-issuer-error"
                className="text-xs text-[var(--color-danger)]"
              >
                {fieldErrors.issuer_url}
              </p>
            ) : (
              <p className="text-[11px] text-[var(--color-fg-subtle)]">
                The OIDC provider's public issuer. Must be https://.
              </p>
            )}
          </div>

          {/* Audience */}
          <div className="space-y-1.5">
            <Label htmlFor="trust-audience">Audience *</Label>
            <Input
              id="trust-audience"
              placeholder="registry"
              value={audience}
              onChange={(e) => {
                setAudience(e.target.value);
                if (fieldErrors.audience) {
                  setFieldErrors({ ...fieldErrors, audience: undefined });
                }
              }}
              aria-invalid={!!fieldErrors.audience}
              aria-describedby={
                fieldErrors.audience ? "trust-audience-error" : undefined
              }
            />
            {fieldErrors.audience ? (
              <p
                id="trust-audience-error"
                className="text-xs text-[var(--color-danger)]"
              >
                {fieldErrors.audience}
              </p>
            ) : null}
          </div>

          {/* Subject pattern */}
          <div className="space-y-1.5">
            <Label htmlFor="trust-subject-pattern">Subject pattern *</Label>
            <Input
              id="trust-subject-pattern"
              placeholder="repo:steveokay/oci-janus:ref:refs/heads/main"
              value={subjectPattern}
              onChange={(e) => {
                setSubjectPattern(e.target.value);
                if (fieldErrors.subject_pattern) {
                  setFieldErrors({
                    ...fieldErrors,
                    subject_pattern: undefined,
                  });
                }
              }}
              aria-invalid={!!fieldErrors.subject_pattern}
              aria-describedby={
                fieldErrors.subject_pattern
                  ? "trust-subject-error"
                  : undefined
              }
              className="font-mono"
            />
            {fieldErrors.subject_pattern ? (
              <p
                id="trust-subject-error"
                className="text-xs text-[var(--color-danger)]"
              >
                {fieldErrors.subject_pattern}
              </p>
            ) : (
              <p className="text-[11px] text-[var(--color-fg-subtle)]">
                Glob matched against the IdP's <code>sub</code> claim.{" "}
                <code>*</code> excludes <code>/</code>, <code>**</code>{" "}
                includes it.
              </p>
            )}
          </div>

          {/* JWKS cache TTL */}
          <div className="space-y-1.5">
            <Label htmlFor="trust-ttl">JWKS cache TTL (seconds)</Label>
            <Input
              id="trust-ttl"
              type="number"
              min={0}
              value={ttlSeconds}
              onChange={(e) => {
                const n = Number(e.target.value);
                setTtlSeconds(Number.isFinite(n) && n >= 0 ? n : 0);
              }}
            />
            <p className="text-[11px] text-[var(--color-fg-subtle)]">
              How long to cache the provider's JWKS. Default 3600.
            </p>
          </div>

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={createTrust.isPending}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              variant="accent"
              loading={createTrust.isPending}
            >
              Create trust
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

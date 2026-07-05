import * as React from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import {
  useTokenPolicy,
  usePutTokenPolicy,
  type PutTokenPolicyInput,
} from "@/lib/api/token-policy";

// PoliciesPanel — live FUT-003 token-policy surface. Replaces
// PoliciesPreview. Mirrors the shape of TrustPanel / HelpersPanel
// (unconditional header, loading / error states below it, populated
// state below that).
//
// Three policy dimensions, each independently toggleable:
//   1. Max token TTL         — hard cap on API-key lifetime
//   2. Force rotation        — require rotation once per interval
//   3. Idle revoke           — auto-revoke keys unused for N days
//
// Semantics vs. the preview:
//   - Each dimension has an Enable/Disable toggle. Unchecked = the field
//     goes to the wire as JSON `null`, which becomes an absent proto
//     Int32Value server-side ("policy disabled for this dimension").
//   - The old "Apply to all keys" copy is retained on the Save button
//     conceptually — grandfathering is what preserves in-flight keys, so
//     the previous "Allow per-key override" checkbox is DROPPED per the
//     plan (grandfathering IS the override mechanism).
//   - "Force rotation" description points to the bell feed (in-app
//     notification) instead of email — email waits on FUT-019.
//
// Numeric input policy:
//   - Range 1..3650 days (the spec's outer bounds).
//   - Client-side validation rejects <=0 or non-integer values inline
//     BEFORE calling the mutation, matching the BE's InvalidArgument
//     guard so the user gets a fast local error rather than a round-trip.

// Range constants — matched to the BE's validation bounds.
const MIN_DAYS = 1;
const MAX_DAYS = 3650;

// SEC-065 (2026-07-01): per-dimension floor. The BE's TokenPolicyService
// applies an extra floor of 7 days on `idle_revoke_days` — set-and-forget
// against a fresh policy shouldn't be able to nuke every workspace key
// on the next hourly tick. Mirror it here so the operator gets a fast
// inline error instead of the raw "Request failed with status code 400"
// from the API.
const PER_FIELD_MIN: Partial<Record<PolicyFieldKey, number>> = {
  idle_revoke_days: 7,
};

// PolicyFieldKey enumerates the three configurable dimensions. Used as
// the discriminator on section state so a single generic renderer can
// drive all three.
type PolicyFieldKey =
  | "max_ttl_days"
  | "rotation_interval_days"
  | "idle_revoke_days";

// SectionState holds the local form state for one dimension. `enabled`
// is the toggle; `value` is the string in the numeric input (kept as a
// string so we can distinguish "empty" from "0" and let the user clear
// the field without React clamping).
interface SectionState {
  enabled: boolean;
  value: string;
}

// FormState is the composed local state — one entry per dimension plus
// a shared client-side validation error.
type FormState = Record<PolicyFieldKey, SectionState>;

// sectionCopy — human-readable strings for each dimension. Keeps the
// render pass compact and lets us keep every copy change in one place.
const SECTION_COPY: Record<
  PolicyFieldKey,
  {
    title: string;
    description: string;
    ariaLabel: string;
  }
> = {
  max_ttl_days: {
    title: "Max token TTL",
    description:
      "No API key may have a lifetime longer than this value. Keys created before the policy is applied are grandfathered until they next rotate.",
    ariaLabel: "Max token TTL in days",
  },
  rotation_interval_days: {
    title: "Force rotation",
    description:
      "Require every API key to be rotated at least once per this interval. You'll see a reminder in the bell feed 14 days before expiry.",
    ariaLabel: "Force rotation interval in days",
  },
  idle_revoke_days: {
    title: "Idle revoke",
    description:
      "Automatically revoke keys that have not been used within this window. A warning is sent in the bell feed 7 days before revocation.",
    ariaLabel: "Idle revocation threshold in days",
  },
};

// toInputState — converts a server value (number | null) into the local
// form shape. null → disabled with a sensible default so the user has a
// starting point once they re-enable the dimension.
function toInputState(
  serverValue: number | null,
  fallback: number,
): SectionState {
  if (serverValue === null || serverValue === undefined) {
    return { enabled: false, value: String(fallback) };
  }
  return { enabled: true, value: String(serverValue) };
}

// defaultsFor — starting values when a dimension is toggled ON but the
// server had null. Mirrors the sensible defaults from PoliciesPreview.
const DEFAULTS: Record<PolicyFieldKey, number> = {
  max_ttl_days: 90,
  rotation_interval_days: 365,
  idle_revoke_days: 30,
};

// validateSection — returns null when the section is valid (either
// disabled, or numeric within range) and an error string otherwise.
// `fieldKey` lets us apply a per-dimension floor (SEC-065: BE enforces
// idle_revoke_days >= 7).
function validateSection(
  fieldKey: PolicyFieldKey,
  state: SectionState,
): string | null {
  if (!state.enabled) return null;
  const trimmed = state.value.trim();
  if (trimmed === "") return "Enter a value";
  const n = Number(trimmed);
  if (!Number.isInteger(n)) return "Value must be a whole number";
  const floor = PER_FIELD_MIN[fieldKey] ?? MIN_DAYS;
  if (n < floor) return `Value must be at least ${floor}`;
  if (n > MAX_DAYS) return `Value must be at most ${MAX_DAYS}`;
  return null;
}

// buildRequestBody — converts the form state into the PUT payload,
// coercing disabled sections to null so the BE persists them as unset.
// require_mfa is a plain boolean (no null/unset state) so it is always
// submitted with its current on/off value.
function buildRequestBody(
  form: FormState,
  requireMfa: boolean,
): PutTokenPolicyInput {
  return {
    max_ttl_days: form.max_ttl_days.enabled
      ? Number(form.max_ttl_days.value)
      : null,
    rotation_interval_days: form.rotation_interval_days.enabled
      ? Number(form.rotation_interval_days.value)
      : null,
    idle_revoke_days: form.idle_revoke_days.enabled
      ? Number(form.idle_revoke_days.value)
      : null,
    require_mfa: requireMfa,
  };
}

// PoliciesPanel — top-level live surface.
export function PoliciesPanel(): React.ReactElement {
  const policy = useTokenPolicy();
  const putPolicy = usePutTokenPolicy();

  // Local form state — seeded from the server response the first time
  // it lands, then owned by the panel from then on so typing doesn't
  // fight the query cache.
  const [form, setForm] = React.useState<FormState | null>(null);
  // require_mfa is a standalone boolean toggle (not one of the three numeric
  // sections), so it lives in its own state seeded from the fetched policy.
  const [requireMfa, setRequireMfa] = React.useState<boolean>(false);
  // UIR-9: save success/error now surface as sonner toasts (see handleSave).
  // Only client-side field validation stays inline — it's tied to a specific
  // input and must persist next to it until the operator fixes the value.
  const [validationError, setValidationError] = React.useState<
    string | null
  >(null);

  // seedForm — sync local state with the server value once (and after
  // every successful PUT, via invalidation + refetch).
  React.useEffect(() => {
    if (policy.data && form === null) {
      setForm({
        max_ttl_days: toInputState(
          policy.data.max_ttl_days,
          DEFAULTS.max_ttl_days,
        ),
        rotation_interval_days: toInputState(
          policy.data.rotation_interval_days,
          DEFAULTS.rotation_interval_days,
        ),
        idle_revoke_days: toInputState(
          policy.data.idle_revoke_days,
          DEFAULTS.idle_revoke_days,
        ),
      });
      // Seed the MFA toggle from the fetched policy at the same time as the
      // numeric sections so the control reflects the persisted value.
      setRequireMfa(policy.data.require_mfa);
    }
  }, [policy.data, form]);

  // handleRequireMfaToggle — flip the MFA enforcement switch.
  function handleRequireMfaToggle(next: boolean): void {
    setRequireMfa(next);
  }

  // handleToggle — flip a section's enabled flag. Clears validation
  // when disabling (a disabled section can't fail validation).
  function handleToggle(key: PolicyFieldKey, enabled: boolean): void {
    if (!form) return;
    setForm({
      ...form,
      [key]: { ...form[key], enabled },
    });
    setValidationError(null);
  }

  // handleValueChange — write-through for a section's numeric input.
  // Kept as a string so the user can clear the field mid-typing.
  function handleValueChange(key: PolicyFieldKey, value: string): void {
    if (!form) return;
    setForm({
      ...form,
      [key]: { ...form[key], value },
    });
    setValidationError(null);
  }

  // handleSave — validates every enabled section, then fires the PUT
  // mutation. Any validation failure short-circuits with an inline
  // banner instead of a round-trip.
  function handleSave(): void {
    if (!form) return;

    // Validate every dimension. First error wins.
    for (const key of Object.keys(form) as PolicyFieldKey[]) {
      const err = validateSection(key, form[key]);
      if (err) {
        setValidationError(`${SECTION_COPY[key].title}: ${err}`);
        return;
      }
    }
    setValidationError(null);

    putPolicy.mutate(buildRequestBody(form, requireMfa), {
      onSuccess: () => {
        // UIR-9: toast the outcome instead of a muted inline banner.
        toast.success("Token policy saved.");
      },
      onError: (err) => {
        const msg =
          err instanceof Error ? err.message : "Failed to save policy";
        toast.error(msg);
      },
    });
  }

  return (
    <div className="space-y-6">
      {/* Header — unconditional; matches TrustPanel / HelpersPanel. */}
      <header className="flex flex-col gap-1">
        <h1 className="font-display text-3xl font-medium tracking-tight">
          Token policies
        </h1>
        <p className="text-sm text-[var(--color-fg-muted)]">
          Enforce workspace-wide limits on token lifetime and rotation
          cadence.
        </p>
      </header>

      {policy.isLoading || form === null ? (
        <div role="status" className="text-sm text-[var(--color-fg-muted)]">
          Loading token policies&hellip;
        </div>
      ) : policy.isError ? (
        <div role="alert" className="text-sm text-[var(--color-danger)]">
          Failed to load token policies. Try refreshing the page.
        </div>
      ) : (
        <>
          <div className="space-y-4">
            <PolicySection
              fieldKey="max_ttl_days"
              state={form.max_ttl_days}
              onToggle={(v) => handleToggle("max_ttl_days", v)}
              onValueChange={(v) => handleValueChange("max_ttl_days", v)}
              suffix="days"
            />
            <PolicySection
              fieldKey="rotation_interval_days"
              state={form.rotation_interval_days}
              onToggle={(v) =>
                handleToggle("rotation_interval_days", v)
              }
              onValueChange={(v) =>
                handleValueChange("rotation_interval_days", v)
              }
              suffix="days"
            />
            <PolicySection
              fieldKey="idle_revoke_days"
              state={form.idle_revoke_days}
              onToggle={(v) => handleToggle("idle_revoke_days", v)}
              onValueChange={(v) =>
                handleValueChange("idle_revoke_days", v)
              }
              suffix="days unused"
            />

            {/* Require MFA — a standalone boolean toggle (TOTP MFA Task 14).
                Unlike the three numeric dimensions it has no value input; it
                is simply on/off, so it uses a Switch rather than a
                PolicySection numeric field. */}
            <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-surface)] p-6">
              <div className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
                <div className="max-w-lg">
                  <h2 className="text-sm font-medium">Require MFA</h2>
                  <p className="mt-0.5 text-xs text-[var(--color-fg-muted)]">
                    Require MFA for all password accounts. Members without an
                    authenticator will be prompted to set one up at next
                    sign-in.
                  </p>
                </div>

                <div className="flex shrink-0 items-center">
                  <Switch
                    checked={requireMfa}
                    onCheckedChange={handleRequireMfaToggle}
                    aria-label="Require MFA for all password accounts"
                  />
                </div>
              </div>
            </div>
          </div>

          {/* Inline field-validation banner only (UIR-9: save success/error
              are toasts now). Validation is tied to a specific input, so it
              stays inline next to the form until the operator fixes it. */}
          {validationError ? (
            <div role="alert" className="text-sm text-[var(--color-danger)]">
              {validationError}
            </div>
          ) : null}

          <div className="flex items-center gap-3 pt-2">
            <Button
              variant="accent"
              onClick={handleSave}
              disabled={putPolicy.isPending}
            >
              {putPolicy.isPending ? "Saving…" : "Save"}
            </Button>
          </div>
        </>
      )}
    </div>
  );
}

// PolicySection — one dimension card. Extracted so the three cards
// stay symmetrical + so the render pass reads at the same abstraction
// level as the plan's "three policy sections" description.
function PolicySection({
  fieldKey,
  state,
  onToggle,
  onValueChange,
  suffix,
}: {
  fieldKey: PolicyFieldKey;
  state: SectionState;
  onToggle: (enabled: boolean) => void;
  onValueChange: (value: string) => void;
  suffix: string;
}): React.ReactElement {
  const copy = SECTION_COPY[fieldKey];
  // SEC-065: per-dimension floor mirrors the BE (idle_revoke_days >= 7).
  const min = PER_FIELD_MIN[fieldKey] ?? MIN_DAYS;
  // Unique id per section — the input + toggle both need aria refs.
  const inputId = `policy-input-${fieldKey}`;
  const toggleId = `policy-toggle-${fieldKey}`;

  return (
    <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-surface)] p-6">
      <div className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
        <div className="max-w-lg">
          <h2 className="text-sm font-medium">{copy.title}</h2>
          <p className="mt-0.5 text-xs text-[var(--color-fg-muted)]">
            {copy.description}
          </p>
        </div>

        <div className="flex shrink-0 flex-col items-end gap-2">
          {/* Enable / Disable toggle. Rendered as a checkbox for
              accessibility — label text flips between Enable / Disable
              to give the operator a clear next-state cue. */}
          <label
            htmlFor={toggleId}
            className="flex cursor-pointer items-center gap-2 text-xs text-[var(--color-fg-muted)]"
          >
            <input
              id={toggleId}
              type="checkbox"
              checked={state.enabled}
              onChange={(e) => onToggle(e.target.checked)}
              aria-label={`${state.enabled ? "Disable" : "Enable"} ${copy.title}`}
            />
            {state.enabled ? "Enabled" : "Disabled"}
          </label>

          <div className="flex items-center gap-2">
            <input
              id={inputId}
              type="number"
              min={min}
              max={MAX_DAYS}
              value={state.value}
              onChange={(e) => onValueChange(e.target.value)}
              disabled={!state.enabled}
              aria-label={copy.ariaLabel}
              className="w-20 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-surface)] px-3 py-1.5 text-right text-sm disabled:opacity-60"
            />
            <span className="text-sm text-[var(--color-fg-muted)]">
              {suffix}
            </span>
          </div>
        </div>
      </div>
    </div>
  );
}

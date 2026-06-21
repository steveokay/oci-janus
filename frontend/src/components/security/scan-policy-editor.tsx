import * as React from "react";
import { useForm, Controller } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { toast } from "sonner";
import { AxiosError } from "axios";
import { ShieldCheck, X } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorState } from "@/components/ui/error-state";
import { EmptyState } from "@/components/ui/empty-state";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import {
  BLOCK_SEVERITY_CHOICES,
  CVE_ID_REGEX,
  SCANNER_PLUGIN_CHOICES,
  usePolicy,
  useUpdatePolicy,
  type BlockOnSeverity,
  type ScannerPlugin,
  type UpdateScanPolicyBody,
} from "@/lib/api/scan-policy";
import { formatAbsoluteDate, formatRelativeDate } from "@/lib/format";
import { cn } from "@/lib/utils";

// Beacon — ScanPolicyEditor (FE-API-018).
//
// Single form for the whole policy: auto-scan toggle, block-on-severity
// radio group, scanner plugin select + version pin, exempt-CVE chip input.
// Save is disabled until the form is dirty so accidental "save" clicks
// don't churn updated_at on the server.

// Zod schema mirrors the BFF allowlist 1:1. Empty string is valid for
// block_on_severity (means "never block"); coerced through a union so
// the radio-group can drive it without an extra transform.
const schema = z.object({
  auto_scan_on_push: z.boolean(),
  block_on_severity: z.enum(["", "CRITICAL", "HIGH", "MEDIUM", "LOW"]),
  scanner_plugin: z.enum(["trivy", "grype"]),
  scanner_version_pin: z
    .string()
    .max(64, "Keep the version pin under 64 characters.")
    .default(""),
  exempt_cves: z
    .array(z.string().regex(CVE_ID_REGEX, "Each CVE must match CVE-YYYY-NNNN."))
    .default([]),
});

type FormValues = z.infer<typeof schema>;

export function ScanPolicyEditor(): React.ReactElement {
  const q = usePolicy();
  const upd = useUpdatePolicy();

  if (q.isError) {
    // 404 on the route ("route disabled" — SCANNER_GRPC_ADDR unset) is the
    // most common reason this surfaces here; the message is intentionally
    // generic so it covers the other failure modes too.
    const status = (q.error as AxiosError | undefined)?.response?.status;
    if (status === 404) {
      return (
        <EmptyState
          icon={<ShieldCheck className="size-5" />}
          title="Scan policies aren't wired on this control plane"
          description="Set SCANNER_GRPC_ADDR on the management BFF and restart to enable per-tenant policy editing."
        />
      );
    }
    return (
      <ErrorState
        title="Couldn't load scan policy"
        description="The scanner service didn't answer. Try again, or check the BFF logs."
        onRetry={() => void q.refetch()}
      />
    );
  }

  if (q.isLoading || !q.data) {
    return (
      <Card>
        <CardHeader>
          <Skeleton className="h-4 w-32" />
        </CardHeader>
        <CardContent className="space-y-6">
          <Skeleton className="h-12 w-full" />
          <Skeleton className="h-24 w-full" />
          <Skeleton className="h-24 w-full" />
        </CardContent>
      </Card>
    );
  }

  return (
    <Editor
      defaults={{
        auto_scan_on_push: q.data.auto_scan_on_push,
        block_on_severity: q.data.block_on_severity,
        scanner_plugin: q.data.scanner_plugin,
        scanner_version_pin: q.data.scanner_version_pin ?? "",
        exempt_cves: q.data.exempt_cves ?? [],
      }}
      updatedAt={q.data.updated_at}
      updatedBy={q.data.updated_by}
      onSubmit={async (v) => {
        try {
          await upd.mutateAsync(v as UpdateScanPolicyBody);
          toast.success("Scan policy updated.");
        } catch (e) {
          const status = (e as AxiosError | undefined)?.response?.status;
          toast.error(
            status === 403
              ? "Admin or owner role on any org in this tenant is required."
              : status === 400
                ? "Backend rejected the policy — check the field values."
                : "Couldn't save the policy. Try again, or check the BFF logs.",
          );
        }
      }}
      saving={upd.isPending}
    />
  );
}

interface EditorProps {
  defaults: FormValues;
  updatedAt?: string;
  updatedBy?: string;
  onSubmit: (values: FormValues) => Promise<void>;
  saving: boolean;
}

function Editor({
  defaults,
  updatedAt,
  updatedBy,
  onSubmit,
  saving,
}: EditorProps): React.ReactElement {
  const {
    control,
    register,
    handleSubmit,
    reset,
    setValue,
    watch,
    formState: { errors, isDirty },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: defaults,
  });

  // Reset the form whenever the server-side defaults shift (e.g. after a
  // successful save). Without this, the "dirty" flag would stay true after
  // a save because RHF's baseline never updates.
  React.useEffect(() => {
    reset(defaults);
  }, [defaults, reset]);

  const exemptCves = watch("exempt_cves") ?? [];

  return (
    <Card>
      <CardHeader className="pb-3">
        <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          Scan policy
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form
          onSubmit={handleSubmit(async (v) => onSubmit(v))}
          className="space-y-8"
          noValidate
        >
          {/* Auto-scan on push — single switch. */}
          <Controller
            name="auto_scan_on_push"
            control={control}
            render={({ field }) => (
              <Section
                title="Auto-scan on push"
                description="When enabled, registry-scanner queues a scan as soon as a manifest finishes uploading."
              >
                <label className="flex items-center justify-between gap-3 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-4 py-3">
                  <div className="min-w-0">
                    <div className="text-sm font-medium text-[var(--color-fg)]">
                      Scan every push automatically
                    </div>
                    <p className="text-xs text-[var(--color-fg-muted)]">
                      Operators can still trigger manual scans on demand from
                      the tag detail page.
                    </p>
                  </div>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                    aria-label="Auto-scan on push"
                  />
                </label>
              </Section>
            )}
          />

          {/* Block on severity — radio group. The empty-string option ("Never
              block") sits at the top so the safest-by-default choice is the
              first thing the operator sees. */}
          <Controller
            name="block_on_severity"
            control={control}
            render={({ field }) => (
              <Section
                title="Block on severity"
                description="Pushes are rejected when the scan reveals findings at the chosen severity (or worse)."
              >
                <div className="space-y-1.5">
                  {BLOCK_SEVERITY_CHOICES.map((choice) => {
                    const selected = field.value === choice.value;
                    return (
                      <RadioCard
                        key={choice.value || "never"}
                        name="block-on-severity"
                        value={choice.value}
                        checked={selected}
                        onChange={(v) => field.onChange(v as BlockOnSeverity)}
                        label={choice.label}
                        description={choice.description}
                      />
                    );
                  })}
                </div>
              </Section>
            )}
          />

          {/* Scanner backend + version pin */}
          <Controller
            name="scanner_plugin"
            control={control}
            render={({ field }) => (
              <Section
                title="Scanner backend"
                description="Which scanner the worker pool routes to. Changing the backend takes effect on the next scheduled scan."
              >
                <div className="grid gap-2 sm:grid-cols-2">
                  {SCANNER_PLUGIN_CHOICES.map((choice) => (
                    <RadioCard
                      key={choice.value}
                      name="scanner-plugin"
                      value={choice.value}
                      checked={field.value === choice.value}
                      onChange={(v) => field.onChange(v as ScannerPlugin)}
                      label={choice.label}
                      description={choice.description}
                    />
                  ))}
                </div>

                <div className="mt-4">
                  <Label htmlFor="scanner_version_pin">Version pin</Label>
                  <Input
                    id="scanner_version_pin"
                    {...register("scanner_version_pin")}
                    placeholder="leave blank to use whatever the scanner pool ships"
                    className="mt-1.5 font-mono text-xs"
                    autoComplete="off"
                    spellCheck={false}
                  />
                  {errors.scanner_version_pin ? (
                    <p className="mt-1.5 text-xs text-[var(--color-danger)]">
                      {errors.scanner_version_pin.message}
                    </p>
                  ) : (
                    <p className="mt-1.5 text-xs text-[var(--color-fg-subtle)]">
                      Free-form. Useful when reproducing a past scan or
                      pinning to a version with a CVE-DB fix.
                    </p>
                  )}
                </div>
              </Section>
            )}
          />

          {/* Exempt CVE chip input */}
          <Section
            title="Exempt CVEs"
            description="CVE-IDs in this list don't count against the block-on-severity gate. Useful for accepted-risk findings."
          >
            <ExemptCvesField
              value={exemptCves}
              onChange={(next) =>
                setValue("exempt_cves", next, {
                  shouldDirty: true,
                  shouldValidate: true,
                })
              }
            />
          </Section>

          {/* Footer — save button + last-updated stamp. */}
          <div className="flex flex-wrap items-center justify-between gap-3 border-t border-[var(--color-border)] pt-4">
            <p className="text-xs text-[var(--color-fg-subtle)]">
              {updatedAt ? (
                <>
                  Updated by{" "}
                  <span className="font-mono text-[var(--color-fg-muted)]">
                    {updatedBy || "unknown"}
                  </span>{" "}
                  <span title={formatAbsoluteDate(updatedAt)}>
                    {formatRelativeDate(updatedAt)}
                  </span>
                </>
              ) : (
                "No prior save recorded — this is the default policy."
              )}
            </p>
            <Button
              type="submit"
              disabled={!isDirty || saving}
              loading={saving}
            >
              {saving ? "Saving" : "Save policy"}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}

// Section — eyebrow + heading + body. Lifts the visual rhythm out of the
// editor so the form code stays focused on field plumbing.
function Section({
  title,
  description,
  children,
}: {
  title: string;
  description: string;
  children: React.ReactNode;
}): React.ReactElement {
  return (
    <section className="space-y-3">
      <div>
        <div className="text-[10px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          {title}
        </div>
        <p className="mt-0.5 text-xs text-[var(--color-fg-muted)]">
          {description}
        </p>
      </div>
      {children}
    </section>
  );
}

// RadioCard — a card-shaped radio option with title + description. Native
// <input type="radio"> visually hidden so we keep keyboard semantics + form
// reset behaviour, while the card chrome is fully custom.
function RadioCard({
  name,
  value,
  checked,
  onChange,
  label,
  description,
}: {
  name: string;
  value: string;
  checked: boolean;
  onChange: (v: string) => void;
  label: string;
  description: string;
}): React.ReactElement {
  return (
    <label
      className={cn(
        "flex cursor-pointer items-start gap-3 rounded-md border bg-[var(--color-surface)] px-3 py-2 transition-colors",
        checked
          ? "border-[var(--color-accent)] bg-[var(--color-accent-subtle)]/40"
          : "border-[var(--color-border)] hover:bg-[var(--color-surface-sunken)]",
      )}
    >
      <input
        type="radio"
        name={name}
        value={value}
        checked={checked}
        onChange={() => onChange(value)}
        className="sr-only"
      />
      <span
        aria-hidden
        className={cn(
          "mt-0.5 grid size-4 shrink-0 place-items-center rounded-full border",
          checked
            ? "border-[var(--color-accent)]"
            : "border-[var(--color-border-strong)]",
        )}
      >
        {checked ? (
          <span className="size-2 rounded-full bg-[var(--color-accent)]" />
        ) : null}
      </span>
      <span className="min-w-0 flex-1">
        <span className="block text-sm font-medium text-[var(--color-fg)]">
          {label}
        </span>
        <span className="block text-xs text-[var(--color-fg-muted)]">
          {description}
        </span>
      </span>
    </label>
  );
}

// ExemptCvesField — chip input. User types a CVE-ID, presses Enter (or
// comma / space), and the chip is added. Validation runs locally so a
// malformed entry never makes it into the form state — keeps the BFF
// from having to reject the save.
function ExemptCvesField({
  value,
  onChange,
}: {
  value: string[];
  onChange: (next: string[]) => void;
}): React.ReactElement {
  const [draft, setDraft] = React.useState("");
  const [error, setError] = React.useState<string | null>(null);

  function commitDraft(): void {
    const candidate = draft.trim().toUpperCase();
    if (!candidate) {
      setError(null);
      return;
    }
    if (!CVE_ID_REGEX.test(candidate)) {
      setError("Use the CVE-YYYY-NNNN format (4–7 digits).");
      return;
    }
    if (value.includes(candidate)) {
      // Silently dedupe — surfacing an error here would feel pedantic.
      setError(null);
      setDraft("");
      return;
    }
    onChange([...value, candidate]);
    setDraft("");
    setError(null);
  }

  function removeAt(i: number): void {
    onChange(value.filter((_, idx) => idx !== i));
  }

  return (
    <div>
      <div className="flex flex-wrap gap-1.5">
        {value.length === 0 ? (
          <span className="text-xs italic text-[var(--color-fg-subtle)]">
            No CVEs exempt yet.
          </span>
        ) : (
          value.map((cve, i) => (
            <span
              key={cve}
              className="inline-flex items-center gap-1 rounded-full border border-[var(--color-accent-border)] bg-[var(--color-accent-subtle)] px-2 py-0.5 font-mono text-[11px] text-[var(--color-accent)]"
            >
              {cve}
              <button
                type="button"
                onClick={() => removeAt(i)}
                aria-label={`Remove ${cve}`}
                className="grid size-3.5 place-items-center rounded-full text-[var(--color-accent)] hover:bg-[var(--color-accent)]/15"
              >
                <X className="size-2.5" />
              </button>
            </span>
          ))
        )}
      </div>
      <div className="mt-2 flex flex-wrap items-center gap-2">
        <Input
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            // Enter / comma / space all commit. Backspace on an empty draft
            // pops the most recent chip — matches the typical chip-input
            // pattern operators expect from password-manager / Slack inputs.
            if (e.key === "Enter" || e.key === "," || e.key === " ") {
              e.preventDefault();
              commitDraft();
            } else if (e.key === "Backspace" && draft === "" && value.length > 0) {
              e.preventDefault();
              removeAt(value.length - 1);
            }
          }}
          onBlur={commitDraft}
          placeholder="CVE-2024-1234"
          className="max-w-[200px] font-mono text-xs"
          spellCheck={false}
          autoCapitalize="characters"
        />
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={commitDraft}
          disabled={draft.trim() === ""}
        >
          Add
        </Button>
      </div>
      {error ? (
        <p className="mt-1.5 text-xs text-[var(--color-danger)]">{error}</p>
      ) : (
        <p className="mt-1.5 text-xs text-[var(--color-fg-subtle)]">
          Press Enter, comma, or space to add. Backspace on an empty box
          removes the last chip.
        </p>
      )}
    </div>
  );
}

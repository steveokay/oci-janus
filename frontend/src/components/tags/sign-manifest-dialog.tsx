import * as React from "react";
import { useForm, Controller } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { toast } from "sonner";
import { PenLine } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useSignManifest } from "@/lib/api/signature";
import { useServiceAccounts } from "@/lib/api/service-accounts";

// SIGNER_ID validation mirrors the backend's `validateSignerID` in
// services/management/internal/handler/sign_manifest.go:
//   non-empty, ≤256 chars, ASCII printable only.
const SIGNER_ID_REGEX = /^[\x20-\x7e]{1,256}$/;

// CUSTOM_SENTINEL is the Select option value that flips the dialog into the
// free-form signer_id mode. It can't collide with a real shadow_user_id (which
// is always a UUID).
const CUSTOM_SENTINEL = "__custom__";

// FUT-009 — the dialog names the signing identity one of two ways, tracked by
// `mode`. The Select path sends the SA's shadow_user_id as service_account_id;
// the free-form path preserves the FE-API-026 signer_id string (cosign CLI
// parity, and the `registry-signer` dev key).
const schema = z
  .object({
    mode: z.enum(["service-account", "custom"]),
    // service_account_id carries the chosen SA's shadow_user_id.
    service_account_id: z.string().optional(),
    signer_id: z.string().optional(),
  })
  .superRefine((val, ctx) => {
    if (val.mode === "service-account") {
      if (!val.service_account_id) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          path: ["service_account_id"],
          message: "Pick a service account.",
        });
      }
      return;
    }
    // custom / free-form path — apply the signer_id allowlist.
    const s = (val.signer_id ?? "").trim();
    if (s.length < 1) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ["signer_id"],
        message: "Signer is required.",
      });
      return;
    }
    if (s.length > 256) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ["signer_id"],
        message: "Keep it under 256 characters.",
      });
      return;
    }
    if (!SIGNER_ID_REGEX.test(s)) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ["signer_id"],
        message:
          "ASCII printable characters only — no control characters or non-ASCII.",
      });
    }
  });

type FormValues = z.infer<typeof schema>;

interface SignManifestDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  org: string;
  repo: string;
  tag: string;
}

// SignManifestDialog — FE-API-026 + FUT-009.
//
// Posts to POST /api/v1/.../sign. Two ways to name the signer:
//   - Service account — pick from the existing useServiceAccounts() list. We
//     send the SA's shadow_user_id as service_account_id; the BFF validates it
//     (exists / enabled / same tenant) and records it as the signer_id.
//   - Custom signer_id — the legacy free-form string, kept for the
//     `registry-signer` dev key and cosign CLI parity.
//
// The signer service owns key material in its Vault backend; this dialog never
// sees a private key. Signing is a destructive write (permanent record), so
// the backend gates it on repo-admin; we surface 403 / 409 / 400 distinctly so
// the toast is actionable.
export function SignManifestDialog({
  open,
  onOpenChange,
  org,
  repo,
  tag,
}: SignManifestDialogProps): React.ReactElement {
  const sign = useSignManifest();
  // The picker offers active (enabled) service accounts. The backend rejects
  // disabled ones anyway; useServiceAccounts() defaults to active-only.
  const { data: serviceAccounts, isLoading: saLoading } = useServiceAccounts();

  const {
    register,
    handleSubmit,
    reset,
    control,
    watch,
    setValue,
    formState: { errors, isSubmitting },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: {
      mode: "service-account",
      service_account_id: "",
      signer_id: "registry-signer",
    },
  });

  const mode = watch("mode");

  React.useEffect(() => {
    if (!open) {
      reset({
        mode: "service-account",
        service_account_id: "",
        signer_id: "registry-signer",
      });
    }
  }, [open, reset]);

  async function onSubmit(values: FormValues): Promise<void> {
    try {
      const sig =
        values.mode === "service-account"
          ? await sign.mutateAsync({
              org,
              repo,
              tag,
              service_account_id: values.service_account_id,
            })
          : await sign.mutateAsync({
              org,
              repo,
              tag,
              signer_id: (values.signer_id ?? "").trim(),
            });
      // For the SA path the returned signer_id is the shadow user_id (a UUID);
      // prefer the chosen SA's display name in the toast when we have it.
      const chosen = serviceAccounts?.find(
        (s) => s.shadow_user_id === values.service_account_id,
      );
      const label =
        values.mode === "service-account" && chosen
          ? chosen.name
          : sig.signer_id;
      toast.success(`Signed ${tag} as ${label}.`);
      onOpenChange(false);
    } catch (e) {
      const status = (e as { response?: { status?: number } })?.response
        ?.status;
      const msg =
        status === 403
          ? "Repo-admin role required to sign manifests."
          : status === 409
            ? "Already signed by this signer — pick a different signer."
            : status === 404
              ? "Signer service is not wired on this BFF (SIGNER_GRPC_ADDR)."
              : status === 400
                ? "Signer rejected the request — check the service account is enabled, or the signer_id is configured."
                : "Couldn't sign. Try again, or check the BFF logs.";
      toast.error(msg);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-[480px]">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <PenLine className="size-4 text-[var(--color-accent)]" />
            Sign manifest
          </DialogTitle>
          <DialogDescription>
            Records a Cosign / Notary v2 signature against the current digest
            of <code className="font-mono">{tag}</code>. Key material stays in
            the signer service's Vault backend — this dialog never sees a
            private key.
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={handleSubmit(onSubmit)} className="space-y-6" noValidate>
          {/* Identity picker — Select over the tenant's service accounts.
              A "Custom signer ID" sentinel flips to the free-form Input so
              operators can still use the dev key / a raw KMS ARN. */}
          <div>
            <Label htmlFor="sa-select" className="mb-2 inline-block">
              Signing identity
            </Label>
            {mode === "service-account" ? (
              <Controller
                control={control}
                name="service_account_id"
                render={({ field }) => (
                  <Select
                    // Always pass a defined value (empty string when unset) so
                    // Radix stays controlled for the component's lifetime —
                    // switching between undefined/defined trips React's
                    // controlled/uncontrolled warning.
                    value={field.value ?? ""}
                    onValueChange={(v) => {
                      if (v === CUSTOM_SENTINEL) {
                        // Switch to the free-form path.
                        setValue("mode", "custom");
                        return;
                      }
                      field.onChange(v);
                    }}
                  >
                    <SelectTrigger
                      id="sa-select"
                      className="w-full"
                      aria-invalid={
                        Boolean(errors.service_account_id) || undefined
                      }
                      aria-label="Signing identity"
                    >
                      <SelectValue
                        placeholder={
                          saLoading
                            ? "Loading service accounts…"
                            : "Select a service account"
                        }
                      />
                    </SelectTrigger>
                    <SelectContent>
                      {(serviceAccounts ?? []).map((sa) => (
                        <SelectItem key={sa.id} value={sa.shadow_user_id}>
                          {sa.name}
                        </SelectItem>
                      ))}
                      <SelectItem value={CUSTOM_SENTINEL}>
                        Custom signer ID…
                      </SelectItem>
                    </SelectContent>
                  </Select>
                )}
              />
            ) : (
              <Input
                id="signer-id"
                autoFocus
                autoComplete="off"
                spellCheck={false}
                className="font-mono"
                aria-invalid={Boolean(errors.signer_id) || undefined}
                {...register("signer_id")}
              />
            )}

            {mode === "service-account" ? (
              errors.service_account_id ? (
                <p className="mt-2 text-xs text-[var(--color-danger)]">
                  {errors.service_account_id.message}
                </p>
              ) : (
                <p className="mt-3 text-xs leading-relaxed text-[var(--color-fg-subtle)]">
                  Sign as one of your workspace service accounts. The signature
                  is recorded against the account so audit trails show a managed
                  identity, not a free-form string.{" "}
                  {(serviceAccounts?.length ?? 0) === 0 && !saLoading ? (
                    <span>
                      No service accounts yet — pick{" "}
                      <span className="text-[var(--color-fg-muted)]">
                        Custom signer ID
                      </span>{" "}
                      or create one under Access.
                    </span>
                  ) : null}
                </p>
              )
            ) : errors.signer_id ? (
              <p className="mt-2 text-xs text-[var(--color-danger)]">
                {errors.signer_id.message}
              </p>
            ) : (
              <p className="mt-3 text-xs leading-relaxed text-[var(--color-fg-subtle)]">
                Defaults to{" "}
                <code className="font-mono text-[var(--color-fg-muted)]">
                  registry-signer
                </code>{" "}
                — the dev key seeded in Vault.{" "}
                <button
                  type="button"
                  className="text-[var(--color-accent)] underline-offset-2 hover:underline"
                  onClick={() => setValue("mode", "service-account")}
                >
                  Use a service account instead
                </button>
                .
              </p>
            )}
          </div>

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={isSubmitting}
            >
              Cancel
            </Button>
            <Button type="submit" loading={isSubmitting} disabled={isSubmitting}>
              {isSubmitting ? "Signing" : "Sign"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

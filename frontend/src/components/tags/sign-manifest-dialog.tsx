import * as React from "react";
import { useForm } from "react-hook-form";
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
import { useSignManifest } from "@/lib/api/signature";

// SIGNER_ID validation mirrors the backend's `validateSignerID` in
// services/management/internal/handler/sign_manifest.go:
//   non-empty, ≤256 chars, ASCII printable only.
const SIGNER_ID_REGEX = /^[\x20-\x7e]{1,256}$/;

const schema = z.object({
  signer_id: z
    .string()
    .min(1, "Signer is required.")
    .max(256, "Keep it under 256 characters.")
    .regex(
      SIGNER_ID_REGEX,
      "ASCII printable characters only — no control characters or non-ASCII.",
    ),
});

type FormValues = z.infer<typeof schema>;

interface SignManifestDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  org: string;
  repo: string;
  tag: string;
}

// SignManifestDialog — FE-API-026.
//
// Posts to POST /api/v1/.../sign with `{ signer_id }`. The signer service
// owns key material in its Vault backend; we only translate the operator's
// chosen signer_id into the SignManifest RPC. Signing is a destructive
// write (it leaves a permanent record), so the backend gates this on
// repo-admin; we surface 403 / 409 / 400 distinctly so the toast is
// actionable.
//
// Default value is `registry-signer` — the dev Vault key seeded by
// infra/docker-compose/vault/init.sh. Productionised tenants would
// pre-fill from a workspace-level signer config (no FE-API for that yet).
export function SignManifestDialog({
  open,
  onOpenChange,
  org,
  repo,
  tag,
}: SignManifestDialogProps): React.ReactElement {
  const sign = useSignManifest();
  const {
    register,
    handleSubmit,
    reset,
    formState: { errors, isSubmitting },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { signer_id: "registry-signer" },
  });

  React.useEffect(() => {
    if (!open) reset({ signer_id: "registry-signer" });
  }, [open, reset]);

  async function onSubmit(values: FormValues): Promise<void> {
    try {
      const sig = await sign.mutateAsync({
        org,
        repo,
        tag,
        signer_id: values.signer_id.trim(),
      });
      toast.success(`Signed ${tag} as ${sig.signer_id}.`);
      onOpenChange(false);
    } catch (e) {
      const status = (e as { response?: { status?: number } })?.response?.status;
      const msg =
        status === 403
          ? "Repo-admin role required to sign manifests."
          : status === 409
            ? "Already signed by this signer — pick a different signer_id."
            : status === 404
              ? "Signer service is not wired on this BFF (SIGNER_GRPC_ADDR)."
              : status === 400
                ? "Signer rejected the request — check the signer_id is configured."
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
          {/* Field stack: label → input is a tight visual group (mb-2),
              then a bigger breath (mt-3) before the hint so the hint
              reads as a separate "footnote" rather than crowding the
              input. Matches the rhythm used by ChangePasswordDialog +
              CreateApiKeyDialog so dialogs feel consistent. */}
          <div>
            <Label htmlFor="signer-id" className="mb-2 inline-block">
              Signer ID
            </Label>
            <Input
              id="signer-id"
              autoFocus
              autoComplete="off"
              spellCheck={false}
              className="font-mono"
              aria-invalid={Boolean(errors.signer_id) || undefined}
              {...register("signer_id")}
            />
            {errors.signer_id ? (
              <p className="mt-2 text-xs text-[var(--color-danger)]">
                {errors.signer_id.message}
              </p>
            ) : (
              <p className="mt-3 text-xs leading-relaxed text-[var(--color-fg-subtle)]">
                Defaults to <code className="font-mono text-[var(--color-fg-muted)]">registry-signer</code>{" "}
                — the dev key seeded in Vault. Override with a fully-qualified
                KMS ARN once configured.
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

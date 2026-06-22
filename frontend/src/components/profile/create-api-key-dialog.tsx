import * as React from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { toast } from "sonner";
import { KeyRound } from "lucide-react";
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
import { useCreateApiKey } from "@/lib/api/api-keys";
import { SecretRevealDialog } from "@/components/webhooks/secret-reveal-dialog";

const schema = z.object({
  name: z
    .string()
    .min(1, "Name is required.")
    .max(64, "Keep it under 64 characters.")
    .regex(
      /^[a-zA-Z0-9 _-]+$/,
      "Letters, digits, spaces, hyphens and underscores only.",
    ),
});

type FormValues = z.infer<typeof schema>;

interface CreateApiKeyDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

// Beacon — CreateApiKeyDialog. Identical flow to the webhook-create dialog:
// form → server returns plaintext secret → chain into SecretRevealDialog.
// Reuses the same masked-by-default reveal primitive that ships in Sprint 5.
//
// B2 fix (sprint-11 maint batch 1): drop the description input — the
// services/auth `createAPIKey` handler never stored description; the FE
// was sending a field the BE silently discarded. The `setSecret(created.key)`
// rename pairs with the FE type alignment in lib/api/api-keys.ts.
export function CreateApiKeyDialog({
  open,
  onOpenChange,
}: CreateApiKeyDialogProps): React.ReactElement {
  const [secret, setSecret] = React.useState<string | null>(null);
  const create = useCreateApiKey();
  const {
    register,
    handleSubmit,
    reset,
    formState: { errors, isSubmitting },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { name: "" },
  });

  React.useEffect(() => {
    if (!open) reset();
  }, [open, reset]);

  async function onSubmit(values: FormValues): Promise<void> {
    try {
      const created = await create.mutateAsync({
        name: values.name.trim(),
      });
      onOpenChange(false);
      setSecret(created.key);
    } catch (e) {
      const status = (e as { response?: { status?: number } })?.response?.status;
      toast.error(
        status === 403
          ? "You don't have permission to create API keys."
          : "Couldn't create key. Try again.",
      );
    }
  }

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <KeyRound className="size-4 text-[var(--color-accent)]" />
              Issue API key
            </DialogTitle>
            <DialogDescription>
              Generate a long-lived credential for a robot account (CI / Terraform /
              scripts). The plaintext secret is shown once on creation.
            </DialogDescription>
          </DialogHeader>

          <form onSubmit={handleSubmit(onSubmit)} className="space-y-4" noValidate>
            <div className="space-y-1.5">
              <Label htmlFor="key-name">Name</Label>
              <Input
                id="key-name"
                placeholder="ci-bot"
                autoFocus
                aria-invalid={Boolean(errors.name) || undefined}
                {...register("name")}
              />
              {errors.name ? (
                <p className="text-xs text-[var(--color-danger)]">
                  {errors.name.message}
                </p>
              ) : null}
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
                {isSubmitting ? "Creating" : "Issue key"}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <SecretRevealDialog
        open={secret !== null}
        onOpenChange={(o) => {
          if (!o) setSecret(null);
        }}
        secret={secret}
        title="API key secret"
        description="Store this in your secrets manager and configure it as the Basic-auth password against the registry. We never store the plaintext — there's no way to retrieve it later."
        onAcknowledge={() => {
          toast.success("API key issued.");
        }}
      />
    </>
  );
}

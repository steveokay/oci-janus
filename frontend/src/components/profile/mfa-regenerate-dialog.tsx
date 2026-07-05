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
import { useRegenerateBackupCodes } from "@/lib/api/mfa";
import { MfaBackupCodes } from "./mfa-backup-codes";

// Same "either field, checked manually" shape as MfaDisableDialog — the
// re-auth contract (password OR a current code) is identical for both
// disable and regenerate, so the schema + submit-time check are mirrored.
const schema = z.object({
  password: z.string().optional(),
  code: z.string().optional(),
});
type FormValues = z.infer<typeof schema>;

interface MfaRegenerateDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

// Beacon — MfaRegenerateDialog.
//
// Two-step flow mirroring MfaEnrollDialog's step machine and dismiss guard:
//
//   reauth → password/code form; POST .../backup-codes/regenerate; on
//            success capture the fresh codes and advance.
//   codes  → render MfaBackupCodes. As in enrolment, this step is NOT
//            dismissable via the corner X / escape / overlay — the codes are
//            served exactly once, so the only exit is the explicit
//            "I've saved these codes" confirm.
export function MfaRegenerateDialog({
  open,
  onOpenChange,
}: MfaRegenerateDialogProps): React.ReactElement {
  const regenerate = useRegenerateBackupCodes();

  const [step, setStep] = React.useState<"reauth" | "codes">("reauth");
  const [codes, setCodes] = React.useState<string[]>([]);

  const {
    register,
    handleSubmit,
    reset,
    formState: { isSubmitting },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { password: "", code: "" },
  });

  // Reset all local state when the dialog closes so a re-open starts clean.
  React.useEffect(() => {
    if (!open) {
      setStep("reauth");
      setCodes([]);
      reset();
    }
  }, [open, reset]);

  async function onSubmit(values: FormValues): Promise<void> {
    const password = values.password?.trim();
    const code = values.code?.trim();
    if (!password && !code) {
      toast.error("Enter your password or a current code.");
      return;
    }
    try {
      const { backup_codes } = await regenerate.mutateAsync({
        ...(password ? { password } : {}),
        ...(code ? { code } : {}),
      });
      setCodes(backup_codes);
      setStep("codes");
    } catch (e) {
      const status = (e as { response?: { status?: number } })?.response
        ?.status;
      toast.error(
        status === 401
          ? "Re-authentication failed. Check your password or code."
          : "Couldn't regenerate backup codes. Try again.",
      );
    }
  }

  // On the codes step the dialog must not be dismissable — same reasoning
  // and same fix as MfaEnrollDialog: gate the root onOpenChange itself (not
  // just escape/overlay) because the corner X renders a Radix Close that
  // calls onOpenChange directly and would otherwise slip past
  // Content-level guards.
  const lockDismiss = step === "codes";
  const guardedOpenChange = (next: boolean) => {
    if (lockDismiss) return; // swallow X / programmatic close while codes are shown
    onOpenChange(next);
  };

  return (
    <Dialog open={open} onOpenChange={guardedOpenChange}>
      <DialogContent
        className="max-w-[480px]"
        onEscapeKeyDown={(e) => {
          if (lockDismiss) e.preventDefault();
        }}
        onInteractOutside={(e) => {
          if (lockDismiss) e.preventDefault();
        }}
      >
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <KeyRound className="size-4 text-[var(--color-accent)]" />
            {step === "codes"
              ? "Save your new backup codes"
              : "Regenerate backup codes"}
          </DialogTitle>
          <DialogDescription>
            {step === "reauth"
              ? "Confirm it's you — this invalidates your existing backup codes and issues a fresh set."
              : "Store these one-time codes before you finish — your previous codes no longer work."}
          </DialogDescription>
        </DialogHeader>

        {/* Step: reauth — password/code form, identical contract to disable. */}
        {step === "reauth" ? (
          <form
            onSubmit={handleSubmit(onSubmit)}
            className="space-y-4"
            noValidate
          >
            <div className="space-y-1.5">
              <Label htmlFor="mfa_regen_password">Password</Label>
              <Input
                id="mfa_regen_password"
                type="password"
                autoComplete="current-password"
                placeholder="Your account password"
                {...register("password")}
              />
            </div>

            <div
              className="flex items-center gap-2 text-xs text-[var(--color-fg-subtle)]"
              aria-hidden
            >
              <span className="h-px flex-1 bg-[var(--color-border)]" />
              or
              <span className="h-px flex-1 bg-[var(--color-border)]" />
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="mfa_regen_code">
                Authenticator or backup code
              </Label>
              <Input
                id="mfa_regen_code"
                autoComplete="one-time-code"
                placeholder="123456 or a backup code"
                {...register("code")}
              />
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
              <Button
                type="submit"
                loading={isSubmitting}
                disabled={isSubmitting}
              >
                {isSubmitting ? "Regenerating" : "Regenerate codes"}
              </Button>
            </DialogFooter>
          </form>
        ) : null}

        {/* Step: codes — save-once panel; confirm is the only exit. */}
        {step === "codes" ? (
          <MfaBackupCodes
            codes={codes}
            onConfirm={() => onOpenChange(false)}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

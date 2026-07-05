import * as React from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { toast } from "sonner";
import { QRCodeSVG } from "qrcode.react";
import { ShieldCheck } from "lucide-react";
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
import { CopyButton } from "@/components/ui/copy-button";
import {
  useMfaEnroll,
  useMfaVerify,
  type MfaEnrollResponse,
} from "@/lib/api/mfa";
import { MfaBackupCodes } from "./mfa-backup-codes";

// Six-digit TOTP code — mirror the backend's expectation (numeric, exactly 6).
const verifySchema = z.object({
  code: z
    .string()
    .regex(/^\d{6}$/, "Enter the 6-digit code from your authenticator."),
});
type VerifyValues = z.infer<typeof verifySchema>;

interface MfaEnrollDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

// Beacon — MfaEnrollDialog.
//
// Three-step TOTP enrolment, mirroring the ChangePasswordDialog contract
// ({ open, onOpenChange }, react-hook-form + zod, mutateAsync + toast):
//
//   scan   → POST /mfa/enroll on open; render the QR + manual secret, "Next".
//   verify → 6-digit code form; POST /mfa/verify; on success capture the
//            one-time backup codes and advance.
//   codes  → render MfaBackupCodes; the dialog is *not* dismissable via the
//            usual cancel/escape/overlay here — the only way out is the
//            "I've saved these codes" confirm, so codes can't be lost by a
//            stray click.
//
// All local state resets when `open` flips to false.
export function MfaEnrollDialog({
  open,
  onOpenChange,
}: MfaEnrollDialogProps): React.ReactElement {
  const enroll = useMfaEnroll();
  const verify = useMfaVerify();

  const [step, setStep] = React.useState<"scan" | "verify" | "codes">("scan");
  const [enrollment, setEnrollment] = React.useState<MfaEnrollResponse | null>(
    null,
  );
  const [backupCodes, setBackupCodes] = React.useState<string[]>([]);

  const {
    register,
    handleSubmit,
    reset,
    formState: { errors, isSubmitting },
  } = useForm<VerifyValues>({
    resolver: zodResolver(verifySchema),
    defaultValues: { code: "" },
  });

  // Kick off enrolment the moment the dialog opens. The enroll mutation
  // returns the secret + otpauth URI we need to draw the QR code. We guard
  // on `open` so re-renders don't fire duplicate enrol calls.
  React.useEffect(() => {
    if (!open) return;
    let cancelled = false;
    void (async () => {
      try {
        const data = await enroll.mutateAsync();
        if (!cancelled) setEnrollment(data);
      } catch {
        if (!cancelled) {
          toast.error("Couldn't start enrolment. Try again.");
          onOpenChange(false);
        }
      }
    })();
    return () => {
      cancelled = true;
    };
    // Only re-run when the dialog transitions open; the mutation + callback
    // identities are stable enough that adding them would just cause churn.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  // Reset every piece of local state when the dialog closes so a re-open
  // starts clean (fresh secret, back to the scan step, empty code field).
  React.useEffect(() => {
    if (!open) {
      setStep("scan");
      setEnrollment(null);
      setBackupCodes([]);
      reset();
    }
  }, [open, reset]);

  // Submit the first TOTP code. Success activates MFA server-side and returns
  // the one-time backup codes; we stash them and move to the codes step.
  async function onVerify(values: VerifyValues): Promise<void> {
    try {
      const { backup_codes } = await verify.mutateAsync(values.code);
      setBackupCodes(backup_codes);
      setStep("codes");
    } catch {
      // 400 = wrong/expired code. Keep the dialog on the verify step so the
      // user can retype without losing progress.
      toast.error("Invalid code. Check your authenticator and try again.");
    }
  }

  // On the codes step the dialog must not be dismissable — the backup codes are
  // shown exactly once, so the only exit is the explicit "I've saved these"
  // confirm. Gate the root onOpenChange itself (not just escape/overlay): the
  // corner X renders a Radix Close that calls onOpenChange directly and would
  // otherwise slip past Content-level guards. Mirrors webhooks/secret-reveal.
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
            <ShieldCheck className="size-4 text-[var(--color-accent)]" />
            {step === "codes"
              ? "Save your backup codes"
              : "Enable two-factor authentication"}
          </DialogTitle>
          <DialogDescription>
            {step === "scan"
              ? "Scan the QR code with an authenticator app (Google Authenticator, 1Password, Authy), then continue."
              : step === "verify"
                ? "Enter the 6-digit code your authenticator shows to confirm it's set up."
                : "Store these one-time codes before you finish — they're your way back in if you lose your authenticator."}
          </DialogDescription>
        </DialogHeader>

        {/* Step: scan — QR + manual secret. */}
        {step === "scan" ? (
          <div className="space-y-4">
            {enrollment ? (
              <>
                <div className="flex justify-center rounded-md border border-[var(--color-border)] bg-white p-4">
                  {/* qrcode.react renders an SVG we can hand the otpauth URI. */}
                  <QRCodeSVG
                    value={enrollment.otpauth_uri}
                    size={180}
                    title="TOTP enrolment QR code"
                  />
                </div>
                <div className="space-y-1.5">
                  <Label>Or enter this code manually</Label>
                  <div className="flex items-center gap-2 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] p-2">
                    <code className="min-w-0 flex-1 select-all break-all font-mono text-sm text-[var(--color-fg)]">
                      {enrollment.secret_base32}
                    </code>
                    <CopyButton
                      value={enrollment.secret_base32}
                      label="Copy"
                    />
                  </div>
                </div>
                <DialogFooter>
                  <Button type="button" onClick={() => setStep("verify")}>
                    Next
                  </Button>
                </DialogFooter>
              </>
            ) : (
              // Awaiting the enroll response — a lightweight placeholder that
              // keeps the dialog height stable while the secret loads.
              <p className="py-8 text-center text-sm text-[var(--color-fg-muted)]">
                Preparing enrolment…
              </p>
            )}
          </div>
        ) : null}

        {/* Step: verify — 6-digit code form. */}
        {step === "verify" ? (
          <form
            onSubmit={handleSubmit(onVerify)}
            className="space-y-4"
            noValidate
          >
            <div className="space-y-1.5">
              <Label htmlFor="mfa_code">Authentication code</Label>
              <Input
                id="mfa_code"
                inputMode="numeric"
                autoComplete="one-time-code"
                maxLength={6}
                placeholder="123456"
                autoFocus
                className="text-center font-mono tracking-[0.4em]"
                aria-invalid={Boolean(errors.code) || undefined}
                {...register("code")}
              />
              {errors.code ? (
                <p className="text-xs text-[var(--color-danger)]">
                  {errors.code.message}
                </p>
              ) : null}
            </div>
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => setStep("scan")}
                disabled={isSubmitting}
              >
                Back
              </Button>
              <Button type="submit" loading={isSubmitting} disabled={isSubmitting}>
                {isSubmitting ? "Verifying" : "Verify & enable"}
              </Button>
            </DialogFooter>
          </form>
        ) : null}

        {/* Step: codes — save-once panel; confirm is the only exit. */}
        {step === "codes" ? (
          <MfaBackupCodes
            codes={backupCodes}
            onConfirm={() => onOpenChange(false)}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

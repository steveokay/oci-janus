import * as React from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { toast } from "sonner";
import { ShieldOff, ShieldAlert } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { PasswordInput } from "@/components/ui/password-input";
import { Label } from "@/components/ui/label";
import { Button } from "@/components/ui/button";
import { useMfaDisable } from "@/lib/api/mfa";

// Both fields are individually optional — the user proves control of the
// account with EITHER the account password OR a currently-valid code (TOTP
// or an unused backup code). We don't encode "at least one" as a zod refine
// tied to a field path; a manual check in onSubmit reads better as a single
// actionable toast rather than a field-level error under one of two inputs.
const schema = z.object({
  password: z.string().optional(),
  code: z.string().optional(),
});
type FormValues = z.infer<typeof schema>;

interface MfaDisableDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

// Beacon — MfaDisableDialog.
//
// Disabling MFA removes a security factor from the account, so the backend
// requires re-auth on the DELETE call. Mirrors ChangePasswordDialog's shape
// ({ open, onOpenChange }, react-hook-form + zod, mutateAsync + toast) with
// a destructive-action treatment (danger accent, warning banner) borrowed
// from DeleteApiKeyDialog.
export function MfaDisableDialog({
  open,
  onOpenChange,
}: MfaDisableDialogProps): React.ReactElement {
  const disable = useMfaDisable();
  const {
    register,
    handleSubmit,
    reset,
    formState: { isSubmitting },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { password: "", code: "" },
  });

  // Reset the form whenever the dialog closes so a re-open starts blank.
  React.useEffect(() => {
    if (!open) reset();
  }, [open, reset]);

  async function onSubmit(values: FormValues): Promise<void> {
    const password = values.password?.trim();
    const code = values.code?.trim();
    if (!password && !code) {
      toast.error("Enter your password or a current code.");
      return;
    }
    try {
      // Omit empty fields entirely rather than sending "" — the backend
      // treats an absent field differently from an empty-string attempt.
      await disable.mutateAsync({
        ...(password ? { password } : {}),
        ...(code ? { code } : {}),
      });
      toast.success("Two-factor authentication disabled");
      onOpenChange(false);
    } catch (e) {
      const status = (e as { response?: { status?: number } })?.response
        ?.status;
      // 401 = the password/code didn't check out. Distinct message so the
      // user knows to retry the re-auth inputs rather than assume a
      // server-side failure.
      toast.error(
        status === 401
          ? "Re-authentication failed. Check your password or code."
          : "Couldn't disable two-factor authentication. Try again.",
      );
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-[440px]">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <ShieldOff className="size-4 text-[var(--color-danger)]" />
            Disable two-factor authentication
          </DialogTitle>
          <DialogDescription>
            Confirm it's you before we turn this off.
          </DialogDescription>
        </DialogHeader>

        {/* Warning — disabling removes the second factor entirely and burns
            the existing backup-code set, so this isn't a reversible toggle. */}
        <div className="flex items-start gap-2 rounded-md border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/10 p-3 text-xs text-[var(--color-danger)]">
          <ShieldAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
          <p>
            Disabling removes your second factor and invalidates your
            existing backup codes. You'll need to re-enrol to turn it back on.
          </p>
        </div>

        <form
          onSubmit={handleSubmit(onSubmit)}
          className="space-y-4"
          noValidate
        >
          <div className="space-y-1.5">
            <Label htmlFor="mfa_disable_password">Password</Label>
            <PasswordInput
              id="mfa_disable_password"
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
            <Label htmlFor="mfa_disable_code">
              Authenticator or backup code
            </Label>
            <Input
              id="mfa_disable_code"
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
              variant="danger"
              loading={isSubmitting}
              disabled={isSubmitting}
            >
              {isSubmitting ? "Disabling" : "Disable two-factor"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

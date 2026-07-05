import * as React from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { toast } from "sonner";
import { ShieldCheck, ArrowRight } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { loginMfa } from "@/lib/api/auth";
import { extractErrorMeta } from "@/lib/api/error";

// Beacon — MfaChallenge.
//
// Second step of the two-step login. Rendered by login.tsx once /login has
// returned an MFA challenge (the password was correct, but the account has a
// TOTP factor). The user enters a 6-digit authenticator code OR — via the
// toggle — a backup code; both hit the same /login/mfa endpoint. On success
// the returned access token is stored (inside loginMfa) and onDone() advances
// to the post-login route. We never log or echo the code itself.

interface MfaChallengeProps {
  // Opaque challenge token minted by /login; authorises the OTP step.
  challengeToken: string;
  // Called after the token has been stored — login.tsx navigates on this.
  onDone: () => void;
}

// TOTP codes are exactly 6 digits. Backup codes are longer and non-numeric
// (e.g. "AAAA-1111"), so we swap the validation when the user toggles modes
// rather than forcing one shape to cover both.
const totpSchema = z.object({
  code: z
    .string()
    .regex(/^\d{6}$/, "Enter the 6-digit code from your authenticator."),
});
const backupSchema = z.object({
  // Relaxed: any non-trivial string. The backend is the real validator; we
  // only guard against empty/whitespace submissions here.
  code: z.string().trim().min(1, "Enter one of your backup codes."),
});
type ChallengeValues = { code: string };

export function MfaChallenge({
  challengeToken,
  onDone,
}: MfaChallengeProps): React.ReactElement {
  // Which credential the user is entering. Backup mode relaxes validation and
  // relabels the field; both submit to the same endpoint.
  const [useBackup, setUseBackup] = React.useState(false);
  const [submitting, setSubmitting] = React.useState(false);

  const {
    register,
    handleSubmit,
    reset,
    formState: { errors },
  } = useForm<ChallengeValues>({
    resolver: zodResolver(useBackup ? backupSchema : totpSchema),
    defaultValues: { code: "" },
  });

  // Toggle mode + clear the field so a half-typed TOTP doesn't linger under a
  // backup-code label (and the resolver swap doesn't flag stale input).
  function toggleMode(): void {
    setUseBackup((prev) => !prev);
    reset({ code: "" });
  }

  async function onSubmit(values: ChallengeValues): Promise<void> {
    setSubmitting(true);
    try {
      await loginMfa(challengeToken, values.code);
      onDone();
    } catch (err) {
      // 401 = wrong/expired code. Keep the form mounted so the user can
      // retype. Never surface the code itself.
      const { code } = extractErrorMeta(err);
      if (code === 401) {
        toast.error("Invalid code. Try again.");
      } else {
        toast.error("Couldn't verify the code. Try again.");
      }
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-6 shadow-[var(--shadow-elevated)]">
      <div className="mb-5 flex flex-col items-center gap-2 text-center">
        <span
          className="grid size-10 place-items-center rounded-lg bg-[var(--color-accent-subtle)] text-[var(--color-accent)]"
          aria-hidden
        >
          <ShieldCheck className="size-5" />
        </span>
        <div>
          <h2 className="font-display text-lg font-medium leading-tight">
            Two-factor authentication
          </h2>
          <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
            {useBackup
              ? "Enter one of your saved backup codes."
              : "Enter the 6-digit code from your authenticator app."}
          </p>
        </div>
      </div>

      <form onSubmit={handleSubmit(onSubmit)} className="space-y-4" noValidate>
        <div className="space-y-1.5">
          <Label htmlFor="mfa_challenge_code">
            {useBackup ? "Backup code" : "Authentication code"}
          </Label>
          <Input
            id="mfa_challenge_code"
            // Backup codes are alphanumeric; only hint numeric keypad for TOTP.
            inputMode={useBackup ? "text" : "numeric"}
            autoComplete="one-time-code"
            maxLength={useBackup ? 32 : 6}
            placeholder={useBackup ? "XXXX-XXXX" : "123456"}
            autoFocus
            className="text-center font-mono tracking-[0.3em]"
            aria-invalid={Boolean(errors.code) || undefined}
            {...register("code")}
          />
          {errors.code ? (
            <p className="text-xs text-[var(--color-danger)]">
              {errors.code.message}
            </p>
          ) : null}
        </div>

        <Button
          type="submit"
          className="w-full"
          loading={submitting}
          disabled={submitting}
        >
          {submitting ? "Verifying" : "Verify"}
          {!submitting ? <ArrowRight className="size-4" /> : null}
        </Button>
      </form>

      {/* Toggle between authenticator and backup-code entry. */}
      <button
        type="button"
        onClick={toggleMode}
        className="mt-4 w-full text-center text-xs text-[var(--color-fg-subtle)] underline hover:text-[var(--color-fg)]"
      >
        {useBackup
          ? "Use your authenticator app instead"
          : "Use a backup code instead"}
      </button>
    </div>
  );
}

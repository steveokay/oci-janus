import * as React from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { toast } from "sonner";
import { KeyRound, Check } from "lucide-react";
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
import { useChangePassword } from "@/lib/api/me";
import { cn } from "@/lib/utils";

// Backend policy: ≥12 chars + at least one upper, one lower, one digit, one
// non-alphanumeric. Same rules as the seed migration; we mirror them here so
// validation fires before the request goes out.
const passwordSchema = z
  .string()
  .min(12, "At least 12 characters.")
  .regex(/[a-z]/, "At least one lowercase letter.")
  .regex(/[A-Z]/, "At least one uppercase letter.")
  .regex(/\d/, "At least one digit.")
  .regex(/[^A-Za-z0-9]/, "At least one non-alphanumeric character.");

const schema = z
  .object({
    current_password: z.string().min(1, "Current password is required."),
    new_password: passwordSchema,
    confirm_password: z.string(),
  })
  .refine((d) => d.new_password === d.confirm_password, {
    path: ["confirm_password"],
    message: "Passwords don't match.",
  })
  .refine((d) => d.new_password !== d.current_password, {
    path: ["new_password"],
    message: "New password must differ from the current one.",
  });

type FormValues = z.infer<typeof schema>;

interface ChangePasswordDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export function ChangePasswordDialog({
  open,
  onOpenChange,
}: ChangePasswordDialogProps): React.ReactElement {
  const change = useChangePassword();
  const {
    register,
    handleSubmit,
    reset,
    watch,
    formState: { errors, isSubmitting },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { current_password: "", new_password: "", confirm_password: "" },
  });

  const newPwd = watch("new_password") ?? "";

  React.useEffect(() => {
    if (!open) reset();
  }, [open, reset]);

  async function onSubmit(values: FormValues): Promise<void> {
    try {
      await change.mutateAsync({
        current_password: values.current_password,
        new_password: values.new_password,
      });
      toast.success("Password changed.");
      onOpenChange(false);
    } catch (e) {
      const status = (e as { response?: { status?: number } })?.response?.status;
      // 401 / 403 → current_password was wrong. We map both to the same
      // "incorrect" message rather than letting the user see policy-leaking
      // detail. Other failures fall to a generic toast.
      const msg =
        status === 401 || status === 403
          ? "Current password is incorrect."
          : status === 400
            ? "New password doesn't meet policy requirements."
            : "Couldn't change password. Try again.";
      toast.error(msg);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-[480px]">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <KeyRound className="size-4 text-[var(--color-accent)]" />
            Change password
          </DialogTitle>
          <DialogDescription>
            Sign in with the new password from your next session. Existing API
            keys keep working — they aren't tied to your password.
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={handleSubmit(onSubmit)} className="space-y-4" noValidate>
          <Field
            id="current_password"
            label="Current password"
            type="password"
            autoComplete="current-password"
            error={errors.current_password?.message}
            {...register("current_password")}
          />
          <Field
            id="new_password"
            label="New password"
            type="password"
            autoComplete="new-password"
            error={errors.new_password?.message}
            {...register("new_password")}
          />
          <PasswordPolicyChecklist value={newPwd} />
          <Field
            id="confirm_password"
            label="Confirm new password"
            type="password"
            autoComplete="new-password"
            error={errors.confirm_password?.message}
            {...register("confirm_password")}
          />

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
              {isSubmitting ? "Updating" : "Update password"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

interface FieldProps extends React.InputHTMLAttributes<HTMLInputElement> {
  id: string;
  label: string;
  error?: string;
}

const Field = React.forwardRef<HTMLInputElement, FieldProps>(function Field(
  { id, label, error, ...props },
  ref,
) {
  return (
    <div className="space-y-1.5">
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        ref={ref}
        aria-invalid={Boolean(error) || undefined}
        {...props}
      />
      {error ? (
        <p className="text-xs text-[var(--color-danger)]">{error}</p>
      ) : null}
    </div>
  );
});

// PasswordPolicyChecklist — five rules ticked off as the user types. Live
// feedback so they don't hit submit blind.
function PasswordPolicyChecklist({ value }: { value: string }): React.ReactElement {
  const rules: Array<{ label: string; ok: boolean }> = [
    { label: "12+ characters", ok: value.length >= 12 },
    { label: "Lowercase letter", ok: /[a-z]/.test(value) },
    { label: "Uppercase letter", ok: /[A-Z]/.test(value) },
    { label: "Digit", ok: /\d/.test(value) },
    { label: "Non-alphanumeric", ok: /[^A-Za-z0-9]/.test(value) },
  ];
  return (
    <ul className="grid grid-cols-2 gap-1 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] p-2 text-[11px]">
      {rules.map((r) => (
        <li
          key={r.label}
          className={cn(
            "flex items-center gap-1.5",
            r.ok
              ? "text-[var(--color-success)]"
              : "text-[var(--color-fg-subtle)]",
          )}
        >
          <Check
            className={cn(
              "size-3",
              r.ok ? "opacity-100" : "opacity-30",
            )}
            aria-hidden
          />
          {r.label}
        </li>
      ))}
    </ul>
  );
}

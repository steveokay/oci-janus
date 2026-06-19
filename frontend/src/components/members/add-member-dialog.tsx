import * as React from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { toast } from "sonner";
import { UserPlus, Crown, Shield, Pencil, Eye } from "lucide-react";
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
import { cn } from "@/lib/utils";
import { ROLES, UUID_REGEX, type Role } from "@/lib/api/members";

const schema = z.object({
  user_id: z
    .string()
    .min(1, "User ID is required.")
    .regex(UUID_REGEX, "Enter a valid UUID."),
  role: z.enum(["owner", "admin", "writer", "reader"]),
});

type FormValues = z.infer<typeof schema>;

interface AddMemberDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  scopeLabel: string;             // e.g. `${org}` or `${org}/${repo}` — appears in title
  // Hook the dialog can call to perform the grant. Returning a Promise keeps
  // the submit button in the loading state until the BFF acknowledges.
  onGrant: (body: { user_id: string; role: Role }) => Promise<void>;
}

const ROLE_DESCRIPTIONS: Record<
  Role,
  { label: string; description: string; icon: React.ComponentType<{ className?: string }> }
> = {
  owner: {
    label: "Owner",
    description: "Full control, including granting other owners.",
    icon: Crown,
  },
  admin: {
    label: "Admin",
    description: "Manage members + repos. Cannot grant ownership.",
    icon: Shield,
  },
  writer: {
    label: "Writer",
    description: "Push and pull. No member or repo management.",
    icon: Pencil,
  },
  reader: {
    label: "Reader",
    description: "Pull only. Read-only across the scope.",
    icon: Eye,
  },
};

export function AddMemberDialog({
  open,
  onOpenChange,
  scopeLabel,
  onGrant,
}: AddMemberDialogProps): React.ReactElement {
  const {
    register,
    handleSubmit,
    setValue,
    watch,
    reset,
    formState: { errors, isSubmitting },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { user_id: "", role: "reader" },
  });
  const role = watch("role");

  async function onSubmit(values: FormValues): Promise<void> {
    try {
      await onGrant({ user_id: values.user_id, role: values.role });
      toast.success(`Granted ${values.role} on ${scopeLabel}.`);
      reset();
      onOpenChange(false);
    } catch (e) {
      const status = (e as { response?: { status?: number } })?.response?.status;
      const message =
        status === 409
          ? "That user already has a role assignment on this scope."
          : status === 403
            ? "You don't have permission to grant roles here."
            : "Couldn't grant. Try again, or check the BFF logs.";
      toast.error(message);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (!o) reset();
        onOpenChange(o);
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <UserPlus className="size-4 text-[var(--color-accent)]" />
            Add member to{" "}
            <span className="font-mono text-[var(--color-fg)]">
              {scopeLabel}
            </span>
          </DialogTitle>
          <DialogDescription>
            Enter the user's UUID and pick a role. A user-search input arrives
            when the auth service exposes a list endpoint.
          </DialogDescription>
        </DialogHeader>
        <form
          onSubmit={handleSubmit(onSubmit)}
          className="space-y-5"
          noValidate
        >
          <div className="space-y-1.5">
            <Label htmlFor="user_id">User ID</Label>
            <Input
              id="user_id"
              placeholder="00000000-0000-0000-0000-000000000000"
              spellCheck={false}
              autoComplete="off"
              className="font-mono"
              aria-invalid={Boolean(errors.user_id) || undefined}
              {...register("user_id")}
            />
            {errors.user_id ? (
              <p className="text-xs text-[var(--color-danger)]">
                {errors.user_id.message}
              </p>
            ) : null}
          </div>

          <div className="space-y-2">
            <Label>Role</Label>
            <div role="radiogroup" className="grid grid-cols-1 gap-1.5">
              {ROLES.map((r) => {
                const { label, description, icon: Icon } = ROLE_DESCRIPTIONS[r];
                const active = role === r;
                return (
                  <button
                    type="button"
                    role="radio"
                    aria-checked={active}
                    key={r}
                    onClick={() =>
                      setValue("role", r, { shouldDirty: true })
                    }
                    className={cn(
                      "group flex items-start gap-3 rounded-md border bg-[var(--color-surface)] px-3 py-2.5 text-left",
                      "transition-colors focus-visible:outline-none",
                      active
                        ? "border-[var(--color-accent)] bg-[var(--color-accent-subtle)]/40"
                        : "border-[var(--color-border)] hover:bg-[var(--color-surface-sunken)]",
                    )}
                  >
                    <span
                      className={cn(
                        "mt-0.5 grid size-7 shrink-0 place-items-center rounded-md",
                        active
                          ? "bg-[var(--color-accent)] text-[var(--color-accent-fg)]"
                          : "bg-[var(--color-surface-sunken)] text-[var(--color-fg-muted)]",
                      )}
                      aria-hidden
                    >
                      <Icon className="size-4" />
                    </span>
                    <div className="min-w-0 flex-1">
                      <div className="text-sm font-medium text-[var(--color-fg)]">
                        {label}
                      </div>
                      <p className="text-xs text-[var(--color-fg-muted)]">
                        {description}
                      </p>
                    </div>
                  </button>
                );
              })}
            </div>
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
              {isSubmitting ? "Granting" : "Grant role"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

import * as React from "react";
import { useNavigate } from "@tanstack/react-router";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { toast } from "sonner";
import { Globe, Lock } from "lucide-react";
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
import { Switch } from "@/components/ui/switch";
import { useCreateRepository } from "@/lib/api/repositories";

// Repository naming mirrors backend rules in CLAUDE.md §7.
// Org: `^[a-z0-9-]{2,64}$`. Repo: `^[a-z0-9]+([._-][a-z0-9]+)*$`, max 128.
const schema = z.object({
  org: z
    .string()
    .min(2, "Org is too short.")
    .max(64, "Org is too long.")
    .regex(/^[a-z0-9-]{2,64}$/, "Lowercase letters, digits and hyphens only."),
  name: z
    .string()
    .min(1, "Name is required.")
    .max(128, "Name is too long.")
    .regex(
      /^[a-z0-9]+([._-][a-z0-9]+)*$/,
      "Lowercase letters, digits, and `._-` only.",
    ),
  is_public: z.boolean(),
});

type FormValues = z.infer<typeof schema>;

interface CreateRepositoryDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export function CreateRepositoryDialog({
  open,
  onOpenChange,
}: CreateRepositoryDialogProps): React.ReactElement {
  const navigate = useNavigate();
  const create = useCreateRepository();
  const {
    register,
    handleSubmit,
    reset,
    setValue,
    watch,
    formState: { errors, isSubmitting },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { org: "", name: "", is_public: false },
  });

  const isPublic = watch("is_public");

  async function onSubmit(values: FormValues): Promise<void> {
    try {
      const created = await create.mutateAsync(values);
      toast.success(`Created ${created.org}/${created.name}.`);
      reset();
      onOpenChange(false);
      void navigate({
        to: "/repositories/$org/$repo",
        params: { org: created.org, repo: created.name },
      });
    } catch (e) {
      const status = (e as { response?: { status?: number } })?.response?.status;
      const message =
        status === 409
          ? "A repository with that name already exists."
          : status === 403
            ? "You don't have permission to create repositories in this org."
            : "Failed to create. Try again, or check the backend logs.";
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
          <DialogTitle>Create repository</DialogTitle>
          <DialogDescription>
            Names follow the OCI convention. Once created, push your first image
            with the pull command shown on the repository page.
          </DialogDescription>
        </DialogHeader>
        <form
          onSubmit={handleSubmit(onSubmit)}
          className="space-y-5"
          noValidate
        >
          <div className="grid grid-cols-[1fr_auto_1fr] items-end gap-2">
            <div className="space-y-1.5">
              <Label htmlFor="org">Organization</Label>
              <Input
                id="org"
                placeholder="acme"
                autoFocus
                aria-invalid={Boolean(errors.org) || undefined}
                {...register("org")}
              />
            </div>
            <div className="pb-2 text-lg text-[var(--color-fg-subtle)]">/</div>
            <div className="space-y-1.5">
              <Label htmlFor="name">Repository</Label>
              <Input
                id="name"
                placeholder="api"
                aria-invalid={Boolean(errors.name) || undefined}
                {...register("name")}
              />
            </div>
          </div>
          {(errors.org || errors.name) ? (
            <p className="text-xs text-[var(--color-danger)]">
              {errors.org?.message ?? errors.name?.message}
            </p>
          ) : null}

          <div className="flex items-center justify-between rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-4 py-3">
            <div className="space-y-0.5">
              <div className="flex items-center gap-2 text-sm font-medium">
                {isPublic ? (
                  <Globe className="size-4 text-[var(--color-accent)]" />
                ) : (
                  <Lock className="size-4 text-[var(--color-fg-muted)]" />
                )}
                {isPublic ? "Public" : "Private"}
              </div>
              <p className="text-xs text-[var(--color-fg-muted)]">
                {isPublic
                  ? "Anyone with the URL can pull images."
                  : "Only members with a role on this repo can pull."}
              </p>
            </div>
            <Switch
              checked={isPublic}
              onCheckedChange={(v) => setValue("is_public", v, { shouldDirty: true })}
              aria-label="Toggle public visibility"
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
            <Button type="submit" loading={isSubmitting} disabled={isSubmitting}>
              {isSubmitting ? "Creating" : "Create repository"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

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
import { useClaimOrg } from "@/lib/api/admin-orgs";
import { useAuthStore } from "@/lib/auth/store";
import { isPlatformAdmin } from "@/lib/auth/jwt";

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
  // FE-API-006 — optional markdown description. We don't constrain length
  // beyond a sane upper bound; the backend caps at 8 KiB.
  description: z.string().max(8_000).optional(),
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
  const claim = useClaimOrg();
  const claims = useAuthStore((s) => s.claims);
  const canClaim = isPlatformAdmin(claims);

  // When the create call returns 403 we stash the typed-in org so the
  // "Claim this org" inline affordance knows what to claim. Platform admins
  // can hit this; everyone else falls back to the existing error toast.
  const [claimableOrg, setClaimableOrg] = React.useState<string | null>(null);

  const {
    register,
    handleSubmit,
    reset,
    setValue,
    watch,
    formState: { errors, isSubmitting },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { org: "", name: "", is_public: false, description: "" },
  });

  const isPublic = watch("is_public");

  async function doCreate(values: FormValues): Promise<void> {
    const created = await create.mutateAsync({
      ...values,
      description: values.description?.trim() || undefined,
    });
    toast.success(`Created ${created.org}/${created.name}.`);
    setClaimableOrg(null);
    reset();
    onOpenChange(false);
    void navigate({
      to: "/repositories/$org/$repo",
      params: { org: created.org, repo: created.name },
    });
  }

  async function onSubmit(values: FormValues): Promise<void> {
    // Any new submit clears a stale "claim this org" prompt — the user may
    // have edited the org field since the last 403.
    setClaimableOrg(null);
    try {
      await doCreate(values);
    } catch (e) {
      const status = (e as { response?: { status?: number } })?.response?.status;
      if (status === 403 && canClaim) {
        // Platform admin trying to bootstrap a fresh org — surface the
        // inline claim affordance instead of the generic permission toast.
        // Trim because the API client + backend both accept the raw value
        // and we want the affordance to refer to exactly what the user typed.
        setClaimableOrg(values.org.trim());
        return;
      }
      const message =
        status === 409
          ? "A repository with that name already exists."
          : status === 403
            ? "You don't have permission to create repositories in this org."
            : "Failed to create. Try again, or check the backend logs.";
      toast.error(message);
    }
  }

  async function onClaimAndRetry(): Promise<void> {
    if (!claimableOrg) return;
    const values = {
      org: watch("org"),
      name: watch("name"),
      is_public: watch("is_public"),
      description: watch("description"),
    } as FormValues;
    try {
      await claim.mutateAsync(claimableOrg);
      toast.success(`Claimed ${claimableOrg} — you now have admin rights here.`);
      setClaimableOrg(null);
      // Transparent retry of the original submission. The grant the BFF just
      // wrote shows up on the next GetUserPermissions call (uncached for the
      // claim path) so hasScopedRole now passes.
      await doCreate(values);
    } catch (e) {
      const status = (e as { response?: { status?: number } })?.response?.status;
      toast.error(
        status === 403
          ? "Claim refused — only platform admins can bootstrap new orgs."
          : "Failed to claim org. Check the backend logs.",
      );
    }
  }

  const busy = isSubmitting || claim.isPending || create.isPending;

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (!o) {
          reset();
          setClaimableOrg(null);
        }
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

          {claimableOrg ? (
            <div className="rounded-md border border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] px-4 py-3">
              <p className="text-sm font-medium">
                You don't have admin on <code>{claimableOrg}</code> yet.
              </p>
              <p className="mt-1 text-xs text-[var(--color-fg-muted)]">
                As a platform admin you can claim it — we'll grant you the
                admin role on this org and retry creating the repository.
              </p>
              <div className="mt-2 flex justify-end">
                <Button
                  type="button"
                  size="sm"
                  loading={claim.isPending}
                  disabled={busy}
                  onClick={onClaimAndRetry}
                >
                  Claim {claimableOrg} and create
                </Button>
              </div>
            </div>
          ) : null}

          <div className="space-y-1.5">
            <Label htmlFor="description">Description (optional)</Label>
            <textarea
              id="description"
              rows={3}
              className="flex w-full rounded-md border border-[var(--color-border-strong)] bg-[var(--color-surface)] px-3 py-2 text-sm leading-relaxed placeholder:text-[var(--color-fg-subtle)] focus-visible:border-[var(--color-accent)] focus-visible:outline-none"
              placeholder="What lives in this repository? Markdown supported (paragraphs only for now)."
              {...register("description")}
            />
          </div>

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
              disabled={busy}
            >
              Cancel
            </Button>
            <Button type="submit" loading={isSubmitting} disabled={busy}>
              {isSubmitting ? "Creating" : "Create repository"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

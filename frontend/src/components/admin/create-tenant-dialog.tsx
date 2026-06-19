import * as React from "react";
import { Building2 } from "lucide-react";
import { toast } from "sonner";
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
import { useCreateTenant } from "@/lib/api/admin-tenants";

const PLANS = ["free", "pro", "enterprise"] as const;

interface CreateTenantDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export function CreateTenantDialog({
  open,
  onOpenChange,
}: CreateTenantDialogProps): React.ReactElement {
  const [name, setName] = React.useState("");
  const [plan, setPlan] = React.useState<string>("free");
  const [nameError, setNameError] = React.useState<string | null>(null);
  const create = useCreateTenant();

  React.useEffect(() => {
    if (!open) {
      setName("");
      setPlan("free");
      setNameError(null);
    }
  }, [open]);

  async function onSubmit(e: React.FormEvent): Promise<void> {
    e.preventDefault();
    const trimmed = name.trim();
    if (!trimmed) {
      setNameError("Tenant name is required.");
      return;
    }
    if (!/^[a-z0-9-]{2,64}$/.test(trimmed)) {
      setNameError(
        "Use 2–64 lowercase letters, digits, or hyphens (no spaces).",
      );
      return;
    }
    try {
      await create.mutateAsync({ name: trimmed, plan });
      toast.success(`Tenant "${trimmed}" created.`);
      onOpenChange(false);
    } catch (e2) {
      const status = (e2 as { response?: { status?: number } })?.response
        ?.status;
      const message =
        status === 409
          ? "That name is already in use — pick a different one."
          : status === 403
            ? "Platform-admin role required."
            : "Couldn't create tenant. Check the BFF logs.";
      toast.error(message);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-[480px]">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Building2 className="size-4 text-[var(--color-accent)]" />
            Create tenant
          </DialogTitle>
          <DialogDescription>
            Provision a new isolated tenant workspace. The name becomes part of
            the tenant&apos;s default registry hostname.
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={(e) => void onSubmit(e)} className="space-y-5">
          <div className="space-y-1.5">
            <Label htmlFor="tenant-name">Name</Label>
            <Input
              id="tenant-name"
              value={name}
              onChange={(e) => {
                setName(e.target.value);
                if (nameError) setNameError(null);
              }}
              autoComplete="off"
              spellCheck={false}
              placeholder="acme"
              className="font-mono"
            />
            {nameError ? (
              <p className="text-xs text-[var(--color-danger)]">{nameError}</p>
            ) : (
              <p className="text-xs text-[var(--color-fg-subtle)]">
                Lowercase letters, digits, hyphens · 2–64 characters
              </p>
            )}
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="tenant-plan">Plan</Label>
            <div className="flex gap-2">
              {PLANS.map((p) => (
                <button
                  key={p}
                  type="button"
                  onClick={() => setPlan(p)}
                  aria-pressed={plan === p}
                  className={
                    plan === p
                      ? "flex-1 rounded-md border border-[var(--color-accent)] bg-[var(--color-accent-subtle)] py-1.5 text-center text-sm font-medium text-[var(--color-accent)]"
                      : "flex-1 rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] py-1.5 text-center text-sm text-[var(--color-fg-muted)] hover:border-[var(--color-border-strong)] hover:text-[var(--color-fg)]"
                  }
                >
                  {p.charAt(0).toUpperCase() + p.slice(1)}
                </button>
              ))}
            </div>
          </div>

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={create.isPending}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              loading={create.isPending}
              disabled={create.isPending}
            >
              {create.isPending ? "Creating" : "Create tenant"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

import * as React from "react";
import { toast } from "sonner";
import { HardDrive } from "lucide-react";
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
import { useSetTenantQuota } from "@/lib/api/admin-tenants";
import { formatBytes } from "@/lib/format";
import type { AdminTenant } from "@/lib/api/admin-tenants";

interface SetQuotaDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  tenant: AdminTenant;
}

// Beacon — SetQuotaDialog.
//
// PUT /api/v1/admin/tenants/{id}/quota expects bytes. Operators think in
// gigabytes / terabytes, so we accept a number-plus-unit input and convert
// to bytes on submit. The list endpoint doesn't carry current quota or
// used bytes (it would require a /stats lookup), so we don't try to render
// a "used vs requested" delta — just take the new value and confirm.
const UNITS = ["GB", "TB"] as const;
type Unit = (typeof UNITS)[number];

const UNIT_MULTIPLIER: Record<Unit, number> = {
  GB: 1024 ** 3,
  TB: 1024 ** 4,
};

export function SetQuotaDialog({
  open,
  onOpenChange,
  tenant,
}: SetQuotaDialogProps): React.ReactElement {
  const [value, setValue] = React.useState<string>("");
  const [unit, setUnit] = React.useState<Unit>("GB");
  const [error, setError] = React.useState<string | null>(null);
  const setQuota = useSetTenantQuota();

  React.useEffect(() => {
    if (!open) {
      setValue("");
      setUnit("GB");
      setError(null);
    }
  }, [open]);

  const parsed = Number(value);
  const valid = Number.isFinite(parsed) && parsed > 0;
  const previewBytes = valid ? parsed * UNIT_MULTIPLIER[unit] : 0;

  async function onSubmit(e: React.FormEvent): Promise<void> {
    e.preventDefault();
    if (!valid) {
      setError("Enter a positive number.");
      return;
    }
    try {
      await setQuota.mutateAsync({
        tenantId: tenant.tenant_id,
        quotaBytes: Math.floor(previewBytes),
      });
      toast.success(`Quota for "${tenant.name}" set to ${value} ${unit}.`);
      onOpenChange(false);
    } catch (e2) {
      const status = (e2 as { response?: { status?: number } })?.response?.status;
      const message =
        status === 403
          ? "Platform-admin role required."
          : status === 404
            ? "Tenant not found (or admin routes disabled on the BFF)."
            : "Couldn't set quota. Check the BFF logs.";
      toast.error(message);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <HardDrive className="size-4 text-[var(--color-accent)]" />
            Set storage quota
          </DialogTitle>
          <DialogDescription>
            Override the storage quota for{" "}
            <span className="font-mono text-[var(--color-fg)]">
              {tenant.name}
            </span>
            . Changes take effect immediately on the next push.
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={(e) => void onSubmit(e)} className="space-y-5">
          <div className="space-y-1.5">
            <Label htmlFor="quota">New quota</Label>
            <div className="flex gap-2">
              <Input
                id="quota"
                type="number"
                inputMode="decimal"
                min="0"
                step="0.1"
                value={value}
                onChange={(e) => {
                  setValue(e.target.value);
                  if (error) setError(null);
                }}
                placeholder="100"
                className="font-mono"
                aria-invalid={Boolean(error) || undefined}
              />
              <div className="flex gap-1 rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] p-1">
                {UNITS.map((u) => {
                  const active = unit === u;
                  return (
                    <button
                      key={u}
                      type="button"
                      onClick={() => setUnit(u)}
                      aria-pressed={active}
                      className={
                        active
                          ? "rounded-sm bg-[var(--color-surface-sunken)] px-3 py-1 text-xs font-medium text-[var(--color-fg)]"
                          : "rounded-sm px-3 py-1 text-xs font-medium text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]"
                      }
                    >
                      {u}
                    </button>
                  );
                })}
              </div>
            </div>
            {error ? (
              <p className="text-xs text-[var(--color-danger)]">{error}</p>
            ) : valid ? (
              // Show the byte-level preview so operators can verify they
              // didn't fat-finger an order of magnitude.
              <p className="text-xs text-[var(--color-fg-subtle)]">
                Will set quota to{" "}
                <span className="font-mono text-[var(--color-fg-muted)]">
                  {formatBytes(previewBytes, 2)}
                </span>
                {" "}({Math.floor(previewBytes).toLocaleString()} bytes).
              </p>
            ) : (
              <p className="text-xs text-[var(--color-fg-subtle)]">
                Enter a value in {unit}.
              </p>
            )}
          </div>

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={setQuota.isPending}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              loading={setQuota.isPending}
              disabled={setQuota.isPending || !valid}
            >
              {setQuota.isPending ? "Saving" : "Update quota"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

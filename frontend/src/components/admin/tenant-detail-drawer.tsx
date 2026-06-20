import * as React from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { toast } from "sonner";
import {
  Building2,
  HardDrive,
  Pencil,
  Save,
  Users,
  X,
  Folders,
  Globe,
  ShieldCheck,
  Upload,
} from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorState } from "@/components/ui/error-state";
import { CopyButton } from "@/components/ui/copy-button";
import {
  TENANT_NAME_REGEX,
  TENANT_PLANS,
  useAdminTenantDetail,
  useUpdateTenant,
  type AdminTenantDetail,
  type TenantPlan,
} from "@/lib/api/admin-tenants";
import { formatAbsoluteDate, formatBytes, formatRelativeDate } from "@/lib/format";
import { cn } from "@/lib/utils";

interface TenantDetailDrawerProps {
  tenantId: string | null;
  onOpenChange: (open: boolean) => void;
}

// FE-API-028 + FE-API-029 — read-mode shows the composed usage breakdown;
// "Edit" toggles into rename / plan-change form. Rendered as a Dialog
// (centered) rather than a slide-in drawer to match the rest of the
// platform-admin surface — there's no drawer primitive in Beacon yet,
// and the modal pattern is well-trodden here.
export function TenantDetailDrawer({
  tenantId,
  onOpenChange,
}: TenantDetailDrawerProps): React.ReactElement {
  const open = Boolean(tenantId);
  const q = useAdminTenantDetail(tenantId ?? undefined);
  const [editing, setEditing] = React.useState(false);

  // Reset edit state whenever the drawer closes or the tenant changes —
  // otherwise re-opening on a different row would briefly show the prior
  // tenant's form values until the new detail loads.
  React.useEffect(() => {
    if (!open) setEditing(false);
  }, [open]);
  React.useEffect(() => {
    setEditing(false);
  }, [tenantId]);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-[600px]">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Building2 className="size-4 text-[var(--color-accent)]" />
            {q.data?.name ?? "Tenant"}
          </DialogTitle>
          <DialogDescription>
            Usage breakdown composed from tenant + metadata + auth + audit.
            Counts degrade to zero rather than block the card on a
            downstream outage.
          </DialogDescription>
        </DialogHeader>

        {q.isError ? (
          <ErrorState
            title="Couldn't load tenant"
            description="Confirm TENANT_GRPC_ADDR is set on the BFF, then retry."
            onRetry={() => void q.refetch()}
          />
        ) : q.isLoading || !q.data ? (
          <SkeletonBody />
        ) : editing ? (
          <EditForm
            t={q.data}
            onCancel={() => setEditing(false)}
            onSaved={() => setEditing(false)}
          />
        ) : (
          <ReadView t={q.data} onEdit={() => setEditing(true)} />
        )}
      </DialogContent>
    </Dialog>
  );
}

function ReadView({
  t,
  onEdit,
}: {
  t: AdminTenantDetail;
  onEdit: () => void;
}): React.ReactElement {
  const quotaPct =
    t.storage_quota_bytes > 0
      ? Math.min(100, (t.storage_used_bytes / t.storage_quota_bytes) * 100)
      : 0;
  return (
    <div className="space-y-5">
      {/* Identity row — plan + slug + host */}
      <div className="flex flex-wrap items-center gap-2">
        <PlanChip plan={t.plan} />
        <Badge tone={t.host_is_custom ? "success" : "accent"}>
          {t.host_is_custom ? (
            <ShieldCheck className="size-3" />
          ) : (
            <Globe className="size-3" />
          )}
          {t.host || `${t.slug}.localhost`}
        </Badge>
        <Button
          variant="outline"
          size="sm"
          onClick={onEdit}
          className="ml-auto"
        >
          <Pencil className="size-3.5" />
          Edit
        </Button>
      </div>

      {/* Identity field rows */}
      <dl className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <Field label="Tenant ID">
          <div className="flex items-center gap-1">
            <code className="font-mono text-xs text-[var(--color-fg)]">
              {t.tenant_id}
            </code>
            <CopyButton value={t.tenant_id} iconOnly />
          </div>
        </Field>
        <Field label="Slug">
          <code className="font-mono text-xs text-[var(--color-fg)]">
            {t.slug || "—"}
          </code>
        </Field>
      </dl>

      {/* Storage card */}
      <div className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] p-3">
        <div className="flex items-center justify-between gap-2">
          <span className="inline-flex items-center gap-1.5 text-xs font-medium text-[var(--color-fg-muted)]">
            <HardDrive className="size-3.5" />
            Storage
          </span>
          <span className="text-xs tabular-nums text-[var(--color-fg)]">
            {formatBytes(t.storage_used_bytes)}
            {t.storage_quota_bytes > 0 ? (
              <span className="text-[var(--color-fg-subtle)]">
                {" "}
                / {formatBytes(t.storage_quota_bytes)} · {quotaPct.toFixed(1)}%
              </span>
            ) : null}
          </span>
        </div>
        {t.storage_quota_bytes > 0 ? (
          <div className="mt-2 h-1.5 w-full overflow-hidden rounded-full bg-[var(--color-surface)]">
            <div
              className={cn(
                "h-full rounded-full transition-all",
                quotaPct >= 90
                  ? "bg-[var(--color-danger)]"
                  : quotaPct >= 75
                    ? "bg-[var(--color-warning)]"
                    : "bg-[var(--color-accent)]",
              )}
              style={{ width: `${quotaPct}%` }}
              aria-hidden
            />
          </div>
        ) : null}
      </div>

      {/* Count tiles — repo / org / user */}
      <div className="grid grid-cols-3 gap-2">
        <CountTile
          icon={<Folders className="size-3.5" />}
          label="Repos"
          value={t.repository_count}
        />
        <CountTile
          icon={<Building2 className="size-3.5" />}
          label="Orgs"
          value={t.organization_count}
        />
        <CountTile
          icon={<Users className="size-3.5" />}
          label="Users"
          value={t.user_count}
        />
      </div>

      {/* Audit pulse */}
      <div className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3 py-2">
        <div className="flex items-center gap-1.5 text-xs font-medium text-[var(--color-fg-muted)]">
          <Upload className="size-3.5" />
          Last push
        </div>
        <div className="mt-0.5 text-sm text-[var(--color-fg)]">
          {t.last_push_at ? (
            <span title={formatAbsoluteDate(t.last_push_at)}>
              {formatRelativeDate(t.last_push_at)}
            </span>
          ) : (
            <span className="text-[var(--color-fg-subtle)]">
              No push activity recorded.
            </span>
          )}
        </div>
      </div>

      <p className="text-[11px] text-[var(--color-fg-subtle)]">
        Created{" "}
        <span title={formatAbsoluteDate(t.created_at)}>
          {formatRelativeDate(t.created_at)}
        </span>
        .
      </p>
    </div>
  );
}

const editSchema = z.object({
  name: z
    .string()
    .min(1, "Name is required.")
    .max(64, "Keep it under 64 characters.")
    .regex(
      TENANT_NAME_REGEX,
      "Use lowercase letters, digits, and hyphens. Must start with a letter or digit.",
    ),
  plan: z.enum(TENANT_PLANS),
});

type EditValues = z.infer<typeof editSchema>;

function EditForm({
  t,
  onCancel,
  onSaved,
}: {
  t: AdminTenantDetail;
  onCancel: () => void;
  onSaved: () => void;
}): React.ReactElement {
  const update = useUpdateTenant();
  const {
    register,
    handleSubmit,
    watch,
    formState: { errors, isSubmitting },
  } = useForm<EditValues>({
    resolver: zodResolver(editSchema),
    defaultValues: { name: t.name, plan: (t.plan as TenantPlan) ?? "free" },
  });
  const values = watch();
  // Track dirty against the source-of-truth values so a no-op submit is
  // disabled — the backend would 400 with "at least one of name or plan"
  // if we sent an empty body, but the better UX is to disable up-front.
  const isDirty = values.name !== t.name || values.plan !== t.plan;

  async function onSubmit(v: EditValues): Promise<void> {
    const body: { name?: string; plan?: string } = {};
    if (v.name !== t.name) body.name = v.name;
    if (v.plan !== t.plan) body.plan = v.plan;
    try {
      await update.mutateAsync({ tenantId: t.tenant_id, body });
      toast.success("Tenant updated.");
      onSaved();
    } catch (e) {
      const status = (e as { response?: { status?: number } })?.response
        ?.status;
      toast.error(
        status === 409
          ? "That name is already taken by another tenant."
          : status === 403
            ? "Platform-admin grant required."
            : status === 400
              ? "Backend rejected the change — check name format."
              : "Couldn't save. Try again, or check the BFF logs.",
      );
    }
  }

  return (
    <form onSubmit={handleSubmit(onSubmit)} className="space-y-5" noValidate>
      <div>
        <Label htmlFor="tenant-name" className="mb-2 inline-block">
          Name
        </Label>
        <Input
          id="tenant-name"
          autoFocus
          autoComplete="off"
          spellCheck={false}
          className="font-mono"
          aria-invalid={Boolean(errors.name) || undefined}
          {...register("name")}
        />
        {errors.name ? (
          <p className="mt-2 text-xs text-[var(--color-danger)]">
            {errors.name.message}
          </p>
        ) : (
          <p className="mt-2 text-xs text-[var(--color-fg-subtle)]">
            Lowercase letters, digits, hyphens. 2–64 chars. Renaming
            recomputes the slug — workspace URLs that embed the old slug
            will need updating.
          </p>
        )}
      </div>

      <div>
        <Label htmlFor="tenant-plan" className="mb-2 inline-block">
          Plan
        </Label>
        <select
          id="tenant-plan"
          {...register("plan")}
          className="block h-10 w-full rounded-md border border-[var(--color-border-strong)] bg-[var(--color-surface)] px-3 text-sm text-[var(--color-fg)]"
        >
          {TENANT_PLANS.map((p) => (
            <option key={p} value={p}>
              {p}
            </option>
          ))}
        </select>
      </div>

      <div className="flex items-center justify-end gap-2 pt-2">
        <Button
          type="button"
          variant="outline"
          onClick={onCancel}
          disabled={isSubmitting}
        >
          <X className="size-4" />
          Cancel
        </Button>
        <Button
          type="submit"
          loading={isSubmitting}
          disabled={isSubmitting || !isDirty}
        >
          <Save className="size-4" />
          Save
        </Button>
      </div>
    </form>
  );
}

function Field({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}): React.ReactElement {
  return (
    <div>
      <dt className="text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
        {label}
      </dt>
      <dd className="mt-1">{children}</dd>
    </div>
  );
}

function CountTile({
  icon,
  label,
  value,
}: {
  icon: React.ReactNode;
  label: string;
  value: number;
}): React.ReactElement {
  return (
    <div className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3 py-2 text-center">
      <div className="flex items-center justify-center gap-1 text-[10px] font-medium uppercase tracking-[0.12em] text-[var(--color-fg-subtle)]">
        {icon}
        {label}
      </div>
      <div className="mt-0.5 font-display text-2xl font-medium leading-none tracking-tight">
        {value.toLocaleString()}
      </div>
    </div>
  );
}

function PlanChip({ plan }: { plan: string }): React.ReactElement {
  const lc = plan.toLowerCase();
  const tone =
    lc === "enterprise" ? "accent" : lc === "pro" ? "success" : "neutral";
  return <Badge tone={tone}>{plan || "—"}</Badge>;
}

function SkeletonBody(): React.ReactElement {
  return (
    <div className="space-y-5">
      <Skeleton className="h-6 w-40" />
      <div className="grid grid-cols-2 gap-3">
        <Skeleton className="h-12 w-full" />
        <Skeleton className="h-12 w-full" />
      </div>
      <Skeleton className="h-14 w-full" />
      <Skeleton className="h-12 w-full" />
    </div>
  );
}

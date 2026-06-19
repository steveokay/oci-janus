import * as React from "react";
import { createFileRoute, redirect } from "@tanstack/react-router";
import { Building2, Plus, ShieldAlert } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { TenantsTable } from "@/components/admin/tenants-table";
import { CreateTenantDialog } from "@/components/admin/create-tenant-dialog";
import { SetQuotaDialog } from "@/components/admin/set-quota-dialog";
import { DeleteTenantDialog } from "@/components/admin/delete-tenant-dialog";
import {
  useAdminTenants,
  type AdminTenant,
} from "@/lib/api/admin-tenants";
import { authStore } from "@/lib/auth/store";
import { isPlatformAdmin } from "@/lib/auth/jwt";

// Platform-admin only. Server is the source of truth (403 if you forge a
// path here), but we redirect non-admins to the dashboard up-front so the
// UI doesn't briefly flash a forbidden state.
export const Route = createFileRoute("/_authenticated/admin/tenants")({
  beforeLoad: () => {
    const claims = authStore.getClaims();
    if (!isPlatformAdmin(claims)) {
      throw redirect({ to: "/" });
    }
  },
  component: AdminTenantsPage,
});

function AdminTenantsPage(): React.ReactElement {
  const { data, isLoading, isError, refetch } = useAdminTenants();
  const [createOpen, setCreateOpen] = React.useState(false);
  const [quotaTarget, setQuotaTarget] = React.useState<AdminTenant | null>(null);
  const [deleteTarget, setDeleteTarget] = React.useState<AdminTenant | null>(
    null,
  );

  const tenants = data ?? [];

  return (
    <div className="space-y-6">
      {/* Platform-admin banner — quiet but unmistakeable. */}
      <div className="flex items-center gap-3 rounded-lg border border-[var(--color-highlight)]/30 bg-[var(--color-highlight)]/5 px-4 py-3">
        <ShieldAlert className="size-4 shrink-0 text-[var(--color-highlight)]" />
        <p className="min-w-0 text-xs text-[var(--color-fg-muted)]">
          You are operating with the{" "}
          <Badge tone="warning" className="font-mono">
            platform-admin
          </Badge>{" "}
          marker grant. Actions on this surface affect tenants across the entire
          control plane.
        </p>
      </div>

      <header className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
            Platform
          </p>
          <h1 className="font-display text-3xl font-medium tracking-tight">
            Tenants
          </h1>
          <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
            {isLoading
              ? "Loading tenants…"
              : `${tenants.length} ${tenants.length === 1 ? "tenant" : "tenants"} provisioned across this control plane`}
          </p>
        </div>
        <Button onClick={() => setCreateOpen(true)}>
          <Plus className="size-4" />
          New tenant
        </Button>
      </header>

      {/* At-a-glance metrics — plan breakdown across all tenants */}
      {!isError && !isLoading && tenants.length > 0 ? (
        <PlanBreakdown tenants={tenants} />
      ) : null}

      {isError ? (
        <ErrorState
          title="Couldn't load tenants"
          description="The admin tenants endpoint didn't answer. Confirm TENANT_GRPC_ADDR is set on the management BFF, then retry."
          onRetry={() => void refetch()}
        />
      ) : !isLoading && tenants.length === 0 ? (
        <EmptyState
          icon={<Building2 className="size-5" />}
          title="No tenants yet"
          description="Provision your first tenant to begin onboarding customers onto this control plane."
          action={
            <Button onClick={() => setCreateOpen(true)}>
              <Plus className="size-4" />
              Create first tenant
            </Button>
          }
        />
      ) : (
        <TenantsTable
          tenants={tenants}
          loading={isLoading}
          onSetQuota={(t) => setQuotaTarget(t)}
          onDelete={(t) => setDeleteTarget(t)}
        />
      )}

      <CreateTenantDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
      />
      {quotaTarget ? (
        <SetQuotaDialog
          open
          onOpenChange={(o) => {
            if (!o) setQuotaTarget(null);
          }}
          tenant={quotaTarget}
        />
      ) : null}
      {deleteTarget ? (
        <DeleteTenantDialog
          open
          onOpenChange={(o) => {
            if (!o) setDeleteTarget(null);
          }}
          tenant={deleteTarget}
        />
      ) : null}
    </div>
  );
}

// Beacon — plan breakdown. Three tiles showing how many tenants on each plan.
// Cheap to derive client-side; gives the operator a sense of fleet shape
// without an extra endpoint.
function PlanBreakdown({
  tenants,
}: {
  tenants: AdminTenant[];
}): React.ReactElement {
  const counts = React.useMemo(() => {
    const c: Record<string, number> = { enterprise: 0, pro: 0, free: 0 };
    for (const t of tenants) {
      const k = (t.plan ?? "").toLowerCase();
      if (k in c) c[k] += 1;
      else c[k] = (c[k] ?? 0) + 1;
    }
    return c;
  }, [tenants]);

  const tiles: Array<{
    plan: string;
    label: string;
    tone: React.ComponentProps<typeof Card>["accentBar"];
  }> = [
    { plan: "enterprise", label: "Enterprise", tone: "accent" },
    { plan: "pro", label: "Pro", tone: "success" },
    { plan: "free", label: "Free", tone: "neutral" },
  ];

  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
      {tiles.map(({ plan, label, tone }) => (
        <Card key={plan} accentBar={tone}>
          <CardHeader className="pb-2">
            <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              {label}
            </CardDescription>
          </CardHeader>
          <CardContent className="pt-0 pb-5">
            <div className="font-display text-3xl font-medium leading-none tracking-tight">
              {(counts[plan] ?? 0).toLocaleString()}
            </div>
          </CardContent>
        </Card>
      ))}
    </div>
  );
}

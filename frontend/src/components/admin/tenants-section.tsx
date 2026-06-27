// REDESIGN-001 Phase 4.2.d — TenantsSection.
//
// Reusable tenant CRUD surface — table + plan breakdown + dialogs +
// detail drawer. Extracted from the old _authenticated.admin.tenants.tsx
// route body so the Platform tab (and the legacy /admin/tenants redirect,
// if it ever needs the surface back) can render the same UI without
// duplicating dialog state.
//
// What's NOT in here:
//   - The platform-admin banner (Settings tab system already gates this).
//   - The page header (the Settings parent route owns the page header).
//   - The "+ New tenant" button is rendered inside this section's local
//     header strip so it stays adjacent to the table.
import * as React from "react";
import { Building2, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
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
import { TenantDetailDrawer } from "@/components/admin/tenant-detail-drawer";
import {
  useAdminTenants,
  type AdminTenant,
} from "@/lib/api/admin-tenants";

export function TenantsSection(): React.ReactElement {
  const { data, isLoading, isError, error, refetch } = useAdminTenants();
  const [createOpen, setCreateOpen] = React.useState(false);
  const [quotaTarget, setQuotaTarget] = React.useState<AdminTenant | null>(null);
  const [deleteTarget, setDeleteTarget] = React.useState<AdminTenant | null>(
    null,
  );
  const [drawerTenantId, setDrawerTenantId] = React.useState<string | null>(
    null,
  );

  const tenants = data ?? [];

  return (
    <section id="tenants" className="space-y-4 scroll-mt-24">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <p className="text-xs font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Tenants
          </p>
          <h2 className="font-display text-xl font-medium">
            Tenant fleet
          </h2>
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
      </div>

      {/* Plan breakdown tiles — cheap client-side derivation. */}
      {!isError && !isLoading && tenants.length > 0 ? (
        <PlanBreakdown tenants={tenants} />
      ) : null}

      {isError ? (
        <ErrorState
          title="Couldn't load tenants"
          description="The admin tenants endpoint didn't answer. Confirm TENANT_GRPC_ADDR is set on the management BFF, then retry."
          error={error}
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
          onView={(t) => setDrawerTenantId(t.tenant_id)}
          onSetQuota={(t) => setQuotaTarget(t)}
          onDelete={(t) => setDeleteTarget(t)}
        />
      )}

      <CreateTenantDialog open={createOpen} onOpenChange={setCreateOpen} />
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
      <TenantDetailDrawer
        tenantId={drawerTenantId}
        onOpenChange={(o) => {
          if (!o) setDrawerTenantId(null);
        }}
      />
    </section>
  );
}

// PlanBreakdown — three tiles showing how many tenants on each plan.
// Cheap to derive client-side; gives the operator a sense of fleet shape
// without an extra endpoint. Lifted verbatim from the old route file.
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

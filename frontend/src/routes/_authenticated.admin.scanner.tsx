import * as React from "react";
import { createFileRoute, redirect } from "@tanstack/react-router";
import { ShieldAlert, ShieldCheck } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Skeleton } from "@/components/ui/skeleton";
import { ScannerHealthCard } from "@/components/admin/scanner/scanner-health-card";
import { AdapterCard } from "@/components/admin/scanner/adapter-card";
import { SetActiveDialog } from "@/components/admin/scanner/set-active-dialog";
import { TestScanPanel } from "@/components/admin/scanner/test-scan-panel";
import {
  useAdapters,
  useTestScan,
  type AdminAdapter,
} from "@/lib/api/admin-scanners";
import { authStore } from "@/lib/auth/store";
import { isPlatformAdmin } from "@/lib/auth/jwt";

// Platform-admin only. Server is the source of truth (403 if you forge the
// path), but we redirect non-admins up-front to avoid a forbidden flash.
// Mirrors _authenticated.admin.tenants.tsx exactly.
export const Route = createFileRoute("/_authenticated/admin/scanner")({
  beforeLoad: () => {
    const claims = authStore.getClaims();
    if (!isPlatformAdmin(claims)) {
      throw redirect({ to: "/" });
    }
  },
  component: AdminScannerPage,
});

function AdminScannerPage(): React.ReactElement {
  const adaptersQ = useAdapters();
  const testScan = useTestScan();
  const [pendingActive, setPendingActive] = React.useState<AdminAdapter | null>(
    null,
  );
  // Persist the test-scan result across re-renders so the panel sticks
  // after the mutation resolves. Cleared via the panel's dismiss button.
  // We piggy-back on the mutation state (`data` / `error` / `isPending`)
  // and only add an explicit `dismissed` flag.
  const [testDismissed, setTestDismissed] = React.useState(false);

  const adapters = adaptersQ.data?.adapters ?? [];
  const active = adapters.find((a) => a.active) ?? null;

  function handleRunTestScan(): void {
    setTestDismissed(false);
    testScan.mutate();
  }

  function handleDismissTestScan(): void {
    setTestDismissed(true);
    testScan.reset();
  }

  const showTestPanel =
    !testDismissed &&
    (testScan.isPending || testScan.data !== undefined || testScan.error);

  return (
    <div className="space-y-6">
      {/* Platform-admin banner — identical copy + colour to /admin/tenants */}
      <div className="flex items-center gap-3 rounded-lg border border-[var(--color-highlight)]/30 bg-[var(--color-highlight)]/5 px-4 py-3">
        <ShieldAlert className="size-4 shrink-0 text-[var(--color-highlight)]" />
        <p className="min-w-0 text-xs text-[var(--color-fg-muted)]">
          You are operating with the{" "}
          <Badge tone="warning" className="font-mono">
            platform-admin
          </Badge>{" "}
          marker grant. Actions on this surface affect every tenant on this
          control plane.
        </p>
      </div>

      <header className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
            Platform
          </p>
          <h1 className="font-display text-3xl font-medium tracking-tight">
            Scanner adapters
          </h1>
          <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
            Pick which scanner backend serves every tenant in this control
            plane. Swaps are in-memory + persisted to{" "}
            <code className="font-mono">scanner_settings</code>; no container
            restart required.
          </p>
        </div>
      </header>

      {/* (A) Health card — pinned to the top so worker-pool state is the
              first thing the operator sees. */}
      <ScannerHealthCard />

      {/* (B) Adapter grid. The 404 fallback (BFF doesn't have
              SCANNER_GRPC_ADDR wired) surfaces as a friendly empty
              state — the brief calls this out explicitly. */}
      {adaptersQ.isError ? (
        <ErrorState
          title="Couldn't load scanner adapters"
          description="The /admin/scanners endpoint didn't answer. Confirm SCANNER_GRPC_ADDR is set on the management BFF, then retry."
          onRetry={() => void adaptersQ.refetch()}
        />
      ) : adaptersQ.isLoading ? (
        <AdapterGridSkeleton />
      ) : adapters.length === 0 ? (
        <EmptyState
          icon={<ShieldCheck className="size-5" />}
          title="No scanner adapters installed"
          description="The scanner service didn't discover any adapter binaries on disk. Drop them into /usr/local/bin and recreate the scanner container."
          action={
            <div className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] p-3 font-mono text-xs text-[var(--color-fg)]">
              docker compose --profile scanner up -d --force-recreate registry-scanner
            </div>
          }
        />
      ) : (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
          {adapters.map((a) => (
            <AdapterCard
              key={a.path}
              adapter={a}
              busy={false}
              testing={a.active && testScan.isPending}
              onMakeActive={() => setPendingActive(a)}
              onRunTestScan={handleRunTestScan}
            />
          ))}
        </div>
      )}

      {/* (C) Test scan panel — appears just below the grid when the
              operator has clicked "Run test scan" on the active card.
              Persists for the session until dismissed. */}
      {showTestPanel ? (
        <TestScanPanel
          result={testScan.data}
          loading={testScan.isPending}
          error={testScan.error}
          activeAdapterName={active?.name ?? ""}
          onRetry={handleRunTestScan}
          onDismiss={handleDismissTestScan}
        />
      ) : null}

      {/* Promote dialog — type-to-confirm by adapter name. */}
      {pendingActive ? (
        <SetActiveDialog
          open
          onOpenChange={(o) => {
            if (!o) setPendingActive(null);
          }}
          adapter={pendingActive}
          currentActive={active}
        />
      ) : null}
    </div>
  );
}

// Skeleton for the adapter grid — 3 placeholder cards matches the xl layout
// and degrades cleanly on narrower viewports.
function AdapterGridSkeleton(): React.ReactElement {
  return (
    <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
      {Array.from({ length: 3 }).map((_, i) => (
        <div
          key={i}
          className="space-y-3 rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-6 shadow-[var(--shadow-card)]"
        >
          <Skeleton className="h-5 w-32" />
          <Skeleton className="h-3 w-20" />
          <Skeleton className="h-3 w-full" />
          <Skeleton className="h-3 w-2/3" />
          <Skeleton className="h-8 w-full" />
        </div>
      ))}
    </div>
  );
}

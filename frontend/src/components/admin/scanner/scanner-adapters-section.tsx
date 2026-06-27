// REDESIGN-001 Phase 4.2.d — ScannerAdaptersSection.
//
// Reusable scanner-adapter management surface — health card + adapter
// grid + test-scan panel + promote dialog. Extracted from the old
// _authenticated.admin.scanner.tsx route body so both the Platform tab
// and (in single mode) the Workspace tab can embed the same UI without
// duplicating dialog/promote state.
//
// What's NOT in here:
//   - Platform-admin banner.
//   - Page header.
//   Both are owned by the Settings parent route.
import * as React from "react";
import { ShieldCheck } from "lucide-react";
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

export function ScannerAdaptersSection(): React.ReactElement {
  const adaptersQ = useAdapters();
  const testScan = useTestScan();
  const [pendingActive, setPendingActive] = React.useState<AdminAdapter | null>(
    null,
  );
  // Persist the test-scan result across re-renders so the panel sticks
  // after the mutation resolves. Cleared via the panel's dismiss button.
  // We piggy-back on the mutation state and only add an explicit
  // `dismissed` flag.
  const [testDismissed, setTestDismissed] = React.useState(false);

  const adapters = adaptersQ.data?.adapters ?? [];
  const active = adapters.find((a) => a.active) ?? null;
  // Active adapter renders first in the grid so the operator's eye lands
  // on it without scanning for the green pill. Sort is stable so two
  // non-active adapters keep their server-supplied ordering.
  const sortedAdapters = React.useMemo(() => {
    return [...adapters].sort((a, b) => {
      if (a.active === b.active) return 0;
      return a.active ? -1 : 1;
    });
  }, [adapters]);

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
    <section id="scanner" className="space-y-4 scroll-mt-24">
      <div>
        <p className="text-xs font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          Vulnerability scanning
        </p>
        <h2 className="font-display text-xl font-medium">Scanner adapters</h2>
        <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
          Pick which scanner backend serves this deployment. Swaps are
          in-memory + persisted to{" "}
          <code className="font-mono text-xs">scanner_settings</code>; no
          container restart required.
        </p>
      </div>

      {/* Health card — pinned to the top so worker-pool state is the
          first thing the operator sees. */}
      <ScannerHealthCard />

      {/* Adapter grid. 404 fallback (BFF doesn't have SCANNER_GRPC_ADDR
          wired) surfaces as a friendly empty state. */}
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
          {sortedAdapters.map((a) => (
            <AdapterCard
              key={a.path}
              adapter={a}
              busy={false}
              testing={a.active && testScan.isPending}
              currentActiveName={active?.name}
              onMakeActive={() => setPendingActive(a)}
              onRunTestScan={handleRunTestScan}
            />
          ))}
        </div>
      )}

      {/* Test scan panel — appears just below the grid when the operator
          has clicked "Run test scan" on the active card. Persists for the
          session until dismissed. */}
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
    </section>
  );
}

// AdapterGridSkeleton — 3 placeholder cards matches the xl layout and
// degrades cleanly on narrower viewports.
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

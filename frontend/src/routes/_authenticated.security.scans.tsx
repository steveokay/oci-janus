// REDESIGN-001 Phase 4.2.e — Security › Scans tab.
//
// Thin wrapper around <ScanHistoryTable />. The table owns its own
// fetching (workspace-wide scan run history), so this route exists
// purely to give it a real URL the operator can share or bookmark.
import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { ScanHistoryTable } from "@/components/security/scan-history-table";

export const Route = createFileRoute("/_authenticated/security/scans")({
  component: ScansTab,
});

function ScansTab(): React.ReactElement {
  return <ScanHistoryTable />;
}

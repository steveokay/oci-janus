// REDESIGN-001 Phase 4.2.e — Security › Reports tab.
//
// Thin wrapper around <ReportsPanel /> (FE-API-019) — async compliance
// report generation (SPDX SBOM + PDF) + download. The panel handles
// the request → poll → download flow itself; this route exists just to
// give it a stable URL the operator can share with auditors.
import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { ReportsPanel } from "@/components/security/reports-panel";

export const Route = createFileRoute("/_authenticated/security/reports")({
  component: ReportsTab,
});

function ReportsTab(): React.ReactElement {
  return <ReportsPanel />;
}

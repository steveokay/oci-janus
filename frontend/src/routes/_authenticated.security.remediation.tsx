// REDESIGN-001 Phase 4.2.e — Security › Remediation tab.
//
// Thin wrapper around <RemediationTable /> (FE-API-017). The table
// rolls findings up by upgrade path so operators can fix the biggest
// chunks of CVEs in one bump. Wrapper only — table owns its own query.
import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { RemediationTable } from "@/components/security/remediation-table";

export const Route = createFileRoute("/_authenticated/security/remediation")({
  component: RemediationTab,
});

function RemediationTab(): React.ReactElement {
  return <RemediationTable />;
}

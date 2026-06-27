// REDESIGN-001 Phase 4.2.e — Security › Vulnerabilities tab.
//
// Thin wrapper around <VulnerabilitiesTable />. The table owns its own
// fetching + filtering (paginates workspace-wide CVEs by severity), so
// this route only exists to give it a stable URL — bookmarkable now that
// the tabs are real sub-routes instead of Radix in-memory state.
import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { VulnerabilitiesTable } from "@/components/security/vulnerabilities-table";

export const Route = createFileRoute(
  "/_authenticated/security/vulnerabilities",
)({
  component: VulnerabilitiesTab,
});

function VulnerabilitiesTab(): React.ReactElement {
  return <VulnerabilitiesTable />;
}

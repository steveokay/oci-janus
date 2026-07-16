// FUT-088 #7 — Settings › Connected Agents (MCP) tab.
//
// Lists the service accounts minted by the one-click MCP connect flow so an
// operator can see which AI agents hold a live key, when each was last used,
// and revoke one in a single click. Admin-gated at the settings-layout level
// (same hasAnyAdminScope gate the SA-admin surface uses); the underlying SA
// API is admin-only server-side as defense in depth.
import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { ConnectedAgentsPanel } from "@/components/settings/ConnectedAgentsPanel";

export const Route = createFileRoute(
  "/_authenticated/settings/connected-agents",
)({
  component: ConnectedAgentsTab,
});

function ConnectedAgentsTab(): React.ReactElement {
  return (
    <div className="space-y-6">
      <ConnectedAgentsPanel />
    </div>
  );
}

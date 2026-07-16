// Settings › Agents tab.
//
// The single home for AI agents (MCP): the one-click connect card to mint a
// read-only agent key, followed by the inventory of already-connected agents
// (MCP-minted service accounts) with last-used + a one-click revoke. Both the
// connect card and the list are admin surfaces — the tab is admin-gated at the
// settings-layout level (hasAnyAdminScope) and each panel additionally renders
// null / relies on admin-only server APIs as defense in depth.
import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { MCPConnectCard } from "@/components/settings/mcp-connect-card";
import { ConnectedAgentsPanel } from "@/components/settings/ConnectedAgentsPanel";

export const Route = createFileRoute("/_authenticated/settings/agents")({
  component: AgentsTab,
});

function AgentsTab(): React.ReactElement {
  return (
    <div className="space-y-6">
      {/* Connect a new AI agent (MCP) — mints a read-only agent key. Moved here
          from Integrations so all agent surfaces live in one place. */}
      <MCPConnectCard />
      {/* Inventory of connected agents (MCP-minted SAs) + one-click revoke. */}
      <ConnectedAgentsPanel />
    </div>
  );
}

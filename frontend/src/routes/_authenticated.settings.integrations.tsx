// FUT-023 Phase 1 — Settings › Integrations tab.
//
// Home for external-SCM integrations. Phase 1 ships the ephemeral PR-registry
// config panel + the active-namespace inventory; future SCM/CI integrations
// stack here. Global-admin only — the tab itself is hidden for non-admins in
// the settings layout, and each panel additionally renders null for non-admins
// as defense in depth.
import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { PRRegistryPanel } from "@/components/settings/pr-registry-panel";
import { PRNamespacesList } from "@/components/settings/pr-namespaces-list";

export const Route = createFileRoute("/_authenticated/settings/integrations")({
  component: IntegrationsTab,
});

function IntegrationsTab(): React.ReactElement {
  return (
    <div className="space-y-6">
      {/* Ephemeral PR-registry config — admin-only; renders null otherwise. */}
      <PRRegistryPanel />
      {/* Active PR-namespace inventory — admin-only; renders null otherwise. */}
      <PRNamespacesList />
    </div>
  );
}

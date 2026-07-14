// UI cleanup 2026-07-05 — Settings › Housekeeping tab.
//
// The storage-cleanup surfaces: garbage collection and retention. Scanning
// config (scan policy + scanner adapters) lives on its own Settings ›
// Scanning tab; identity/delivery/sign-in/lifecycle + posture live on
// Workspace.
//
// The platform is single-tenant (workspace == deployment == platform), so
// these storage-cleanup surfaces live on their own Settings tab — there is
// no separate Platform console. This component renders the sections
// unconditionally for whoever reaches the URL.
import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { GCCard } from "@/components/admin/gc-card";
import { RetentionCard } from "@/components/admin/retention-card";
import { SectionAnchorNav } from "@/components/ui/section-anchor-nav";

export const Route = createFileRoute("/_authenticated/settings/housekeeping")({
  component: HousekeepingTab,
});

function HousekeepingTab(): React.ReactElement {
  return (
    <div className="space-y-6">
      {/* Anchor chips for the two cleanup sections below. */}
      <SectionAnchorNav
        ariaLabel="Housekeeping sections"
        items={[
          { id: "gc", label: "Garbage collection" },
          { id: "retention", label: "Retention" },
        ]}
      />

      {/* Garbage collection — status + Recent runs + run-now. */}
      <section id="gc" className="space-y-4 scroll-mt-24">
        <GCCard />
      </section>

      {/* Retention — status + Recent runs. */}
      <section id="retention" className="space-y-4 scroll-mt-24">
        <RetentionCard />
      </section>
    </div>
  );
}

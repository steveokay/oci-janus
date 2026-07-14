// UI cleanup 2026-07-05 — Settings › Scanning tab.
//
// Groups the vulnerability-scanning configuration on its own tab: the
// tenant-wide scan policy (what severities block) and the scanner adapters
// (which engines run + their health). Split out of Housekeeping so that tab
// stays scoped to storage cleanup (GC + retention).
//
// The platform is single-tenant (same posture as Housekeeping), so these
// scanning surfaces live on their own Settings tab — there is no separate
// Platform console. This component renders the sections for whoever reaches
// the URL.
import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { ScanPolicyEditor } from "@/components/security/scan-policy-editor";
import { ScannerAdaptersSection } from "@/components/admin/scanner/scanner-adapters-section";
import { SectionAnchorNav } from "@/components/ui/section-anchor-nav";

export const Route = createFileRoute("/_authenticated/settings/scanning")({
  component: ScanningTab,
});

function ScanningTab(): React.ReactElement {
  return (
    <div className="space-y-6">
      {/* Anchor chips for the two scanning sections. ScannerAdaptersSection
          carries its own id="scanner"; the scan policy is wrapped here. */}
      <SectionAnchorNav
        ariaLabel="Scanning sections"
        items={[
          { id: "scan-policy", label: "Scan policy" },
          { id: "scanner", label: "Scanner adapters" },
        ]}
      />

      {/* Tenant-wide scan policy. RBAC-aware: GET is reader-grade, PUT
          requires admin on ≥1 org; non-admins get a disabled Save. */}
      <section id="scan-policy" className="space-y-4 scroll-mt-24">
        <ScanPolicyEditor />
      </section>

      {/* Scanner adapters (renders its own id="scanner"). */}
      <ScannerAdaptersSection />
    </div>
  );
}

// REDESIGN-001 Phase 4.2.e — Security › Policies tab.
//
// Thin wrapper around <ScanPolicyEditor /> (FE-API-018) — the
// block-on-severity scan policy editor. The component handles its own
// fetch + PUT; this route exists to give it a stable URL. Note: the
// editor is ALSO embedded in /settings/workspace as part of Phase 4.2.c —
// that's intentional (Workspace is the "everything you'd configure for
// the workspace" hub) and this Security tab is the "I'm in posture mode,
// let me tweak the policy without leaving" entrypoint.
//
// Server-side authz still gates the PUT — non-admin callers see the
// editor read-only, which matches the behaviour of the embedded copy.
import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { ScanPolicyEditor } from "@/components/security/scan-policy-editor";

export const Route = createFileRoute("/_authenticated/security/policies")({
  component: PoliciesTab,
});

function PoliciesTab(): React.ReactElement {
  return <ScanPolicyEditor />;
}

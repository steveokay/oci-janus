import { createFileRoute } from "@tanstack/react-router";
import { PoliciesPanel } from "@/components/access/PoliciesPanel";

// /api-keys/policies — token-policy enforcement surface (FUT-003).
// Live as of Sprint 12 (2026-07-01). The layout-level admin gate in
// AccessSubNav hides this link for non-admins; no additional beforeLoad
// guard is required here.
export const Route = createFileRoute("/_authenticated/api-keys/policies")({
  component: PoliciesPanel,
});

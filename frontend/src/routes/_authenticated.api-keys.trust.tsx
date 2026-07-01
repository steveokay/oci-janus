import { createFileRoute } from "@tanstack/react-router";
import { TrustPanel } from "@/components/access/TrustPanel";

// /api-keys/trust — federated workload-identity surface (FUT-001).
// Live as of Sprint 11 — TrustPreview retired 2026-07-01. The
// layout-level admin gate in AccessSubNav hides this link for non-admins;
// no additional beforeLoad guard is required here.
export const Route = createFileRoute("/_authenticated/api-keys/trust")({
  component: TrustPanel,
});

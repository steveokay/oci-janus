import { createFileRoute } from "@tanstack/react-router";
import { TrustPreview } from "@/components/access/previews/TrustPreview";

// /api-keys/trust — federated workload-identity preview surface (FUT-001).
// Ships as a live route in Sprint 11. The layout-level admin gate in
// AccessSubNav hides this link for non-admins; no additional beforeLoad
// guard is required here.
export const Route = createFileRoute("/_authenticated/api-keys/trust")({
  component: TrustPreview,
});

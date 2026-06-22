import { createFileRoute } from "@tanstack/react-router";
import { PoliciesPreview } from "@/components/access/previews/PoliciesPreview";

// /api-keys/policies — token-policy enforcement preview surface (FUT-003).
// Ships as a live route in Sprint 12. The layout-level admin gate in
// AccessSubNav hides this link for non-admins; no additional beforeLoad
// guard is required here.
export const Route = createFileRoute("/_authenticated/api-keys/policies")({
  component: PoliciesPreview,
});

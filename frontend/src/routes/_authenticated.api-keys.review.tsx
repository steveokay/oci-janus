import { createFileRoute } from "@tanstack/react-router";
import { ReviewPreview } from "@/components/access/previews/ReviewPreview";

// /api-keys/review — periodic access-review preview surface (FUT-004).
// Ships as a live route in Sprint 12. The layout-level admin gate in
// AccessSubNav hides this link for non-admins; no additional beforeLoad
// guard is required here.
export const Route = createFileRoute("/_authenticated/api-keys/review")({
  component: ReviewPreview,
});

import { createFileRoute } from "@tanstack/react-router";
import { ReviewPanel } from "@/components/access/ReviewPanel";

// /api-keys/review — periodic access-review surface (FUT-004). Live
// as of Sprint 12 — the previous ReviewPreview has been removed. The
// layout-level admin gate in AccessSubNav hides this link for
// non-admins; no additional beforeLoad guard is required here.
export const Route = createFileRoute("/_authenticated/api-keys/review")({
  component: ReviewPanel,
});

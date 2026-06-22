import { createFileRoute } from "@tanstack/react-router";
import { HelpersPreview } from "@/components/access/previews/HelpersPreview";

// /api-keys/helpers — credential-helper snippet preview surface (FUT-002).
// Ships as a live route in Sprint 11. The layout-level admin gate in
// AccessSubNav hides this link for non-admins; no additional beforeLoad
// guard is required here.
export const Route = createFileRoute("/_authenticated/api-keys/helpers")({
  component: HelpersPreview,
});

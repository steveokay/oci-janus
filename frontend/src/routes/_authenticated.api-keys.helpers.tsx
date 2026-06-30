import { createFileRoute } from "@tanstack/react-router";
import { HelpersPanel } from "@/components/access/HelpersPanel";

// /api-keys/helpers — live credential-helpers surface (FUT-002).
// The layout-level admin gate in AccessSubNav hides this link for non-admins;
// no additional beforeLoad guard is required here.
export const Route = createFileRoute("/_authenticated/api-keys/helpers")({
  component: HelpersPanel,
});

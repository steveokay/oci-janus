import * as React from "react";
import { createFileRoute, Outlet } from "@tanstack/react-router";
import { AccessHubLayout } from "@/components/access/AccessHubLayout";

// /api-keys hub — layout shell for the workspace credential surface.
//
// Previously this file was a single-page component rendering `ApiKeysSection`
// directly. It has been converted to a hub layout (FE-API-048 T24) so that
// child routes — personal keys (index), service accounts, activity, and
// future preview surfaces (FUT-001..FUT-004) — each render in the right pane
// of `AccessHubLayout` while the `AccessSubNav` rail persists across navigations.
//
// The existing page content has moved to:
//   `_authenticated.api-keys.index.tsx`  ← /api-keys (exact match)
//
// T25-T28 will create:
//   `_authenticated.api-keys.service-accounts.tsx`  ← /api-keys/service-accounts
//   `_authenticated.api-keys.activity.tsx`           ← /api-keys/activity
//   `_authenticated.api-keys.trust.tsx`              ← /api-keys/trust  (FUT-001 preview)
//   `_authenticated.api-keys.helpers.tsx`            ← /api-keys/helpers (FUT-002 preview)
//   `_authenticated.api-keys.policies.tsx`           ← /api-keys/policies (FUT-003 preview)
//   `_authenticated.api-keys.review.tsx`             ← /api-keys/review  (FUT-004 preview)
export const Route = createFileRoute("/_authenticated/api-keys")({
  component: ApiKeysHub,
});

function ApiKeysHub(): React.ReactElement {
  return (
    <AccessHubLayout>
      <Outlet />
    </AccessHubLayout>
  );
}

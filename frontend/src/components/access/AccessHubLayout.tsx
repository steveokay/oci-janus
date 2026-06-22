import * as React from "react";
import { AccessSubNav } from "./AccessSubNav";

interface AccessHubLayoutProps {
  children: React.ReactNode;
}

// AccessHubLayout — page shell for the /api-keys hub.
// Renders a fixed-width left sub-nav rail (12 rem / w-48) alongside the
// router-driven right pane. The hub root route (`_authenticated.api-keys.tsx`)
// wraps `<Outlet />` with this layout; every child route (personal keys,
// service accounts, activity, preview surfaces) renders into the right pane
// without re-mounting the sub-nav on navigation.
export function AccessHubLayout({
  children,
}: AccessHubLayoutProps): React.ReactElement {
  return (
    <div className="flex gap-8">
      {/* Left: vertical sub-nav rail. */}
      <AccessSubNav />

      {/* Right: child route content. `min-w-0` prevents flex children that
          contain wide tables / code blocks from blowing out the layout. */}
      <div className="flex-1 min-w-0">{children}</div>
    </div>
  );
}

import * as React from "react";
import { Sidebar } from "./sidebar";
import { Topbar } from "./topbar";

interface AppShellProps {
  breadcrumb?: React.ReactNode;
  children: React.ReactNode;
}

// Beacon — AppShell. Persistent layout for every authenticated route.
// Sidebar is fixed-width 248px (lg+); content fills the rest. The Topbar
// breadcrumb slot is filled per-route.
export function AppShell({
  breadcrumb,
  children,
}: AppShellProps): React.ReactElement {
  return (
    <div className="flex h-full min-h-screen">
      <Sidebar />
      <div className="flex min-w-0 flex-1 flex-col">
        <Topbar breadcrumb={breadcrumb} />
        <main className="flex-1 overflow-y-auto px-6 py-6 lg:px-10 lg:py-8">
          <div className="mx-auto w-full max-w-[1440px]">{children}</div>
        </main>
      </div>
    </div>
  );
}

import * as React from "react";
import { Sidebar } from "./sidebar";
import { Topbar } from "./topbar";
import { Footer } from "./footer";
import { MobileNav } from "./mobile-nav";

interface AppShellProps {
  breadcrumb?: React.ReactNode;
  children: React.ReactNode;
}

// Beacon — AppShell. Persistent layout for every authenticated route.
// Sidebar is fixed-width 248px (lg+); content fills the rest. The Topbar
// breadcrumb slot is filled per-route. The Footer pins to the bottom of
// the right column as a status bar that's always in view.
//
// REDESIGN-001 Phase 4.6 — below `lg` the desktop sidebar is hidden and
// MobileNav replaces it. AppShell owns the drawer's open/close state so
// the Topbar hamburger trigger and the drawer Content share a single
// React-level controller; pushing this further down (e.g. into Topbar)
// would force MobileNav to mount inside Topbar's tree, where its Portal
// would still work but the test surface would be muddier.
//
// The "Skip to main" link is the very first focusable element. It's
// visually hidden until focused, then surfaces above the topbar so
// keyboard users can jump past the sidebar/topbar in a single Tab.
// The `<main>` element carries `id="main"` so the anchor resolves.
export function AppShell({
  breadcrumb,
  children,
}: AppShellProps): React.ReactElement {
  const [navOpen, setNavOpen] = React.useState(false);

  return (
    <div className="flex h-full min-h-screen">
      {/* a11y: skip-link must be the first focusable element so a single
          Tab from page load lands here. `sr-only` keeps it invisible until
          focused; `focus:not-sr-only` flips it into a visible chip. */}
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:fixed focus:left-3 focus:top-3 focus:z-[60] focus:rounded-md focus:bg-[var(--color-accent)] focus:px-3 focus:py-1.5 focus:text-sm focus:font-medium focus:text-[var(--color-accent-fg)] focus:shadow-[var(--shadow-floating)]"
      >
        Skip to main content
      </a>

      <Sidebar />
      <MobileNav open={navOpen} onOpenChange={setNavOpen} />

      <div className="flex min-w-0 flex-1 flex-col">
        <Topbar
          breadcrumb={breadcrumb}
          onOpenMobileNav={() => setNavOpen(true)}
        />
        <main
          id="main"
          tabIndex={-1}
          className="flex-1 overflow-y-auto px-6 py-6 lg:px-10 lg:py-8"
        >
          <div className="mx-auto w-full max-w-[1440px]">{children}</div>
        </main>
        <Footer />
      </div>
    </div>
  );
}

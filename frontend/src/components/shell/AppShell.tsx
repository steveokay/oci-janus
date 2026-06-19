/**
 * AppShell — the authenticated app frame. Renders the sidebar on the
 * left, topbar on top of the content slot, and a scrolling main area
 * underneath.
 *
 * Page components render into `children` and own their own internal
 * padding + spacing. The shell only owns layout chrome — it deliberately
 * avoids opinionated content padding so pages with full-bleed elements
 * (tables, large headers) don't have to fight a parent gutter.
 */
import type { ReactNode } from 'react'
import { Sidebar } from './Sidebar'
import { Topbar } from './Topbar'

export function AppShell({ children }: { children: ReactNode }) {
  return (
    <div className="min-h-screen flex bg-surface-muted">
      <Sidebar />
      <div className="flex-1 flex flex-col min-w-0">
        <Topbar />
        <main className="flex-1 overflow-y-auto">{children}</main>
      </div>
    </div>
  )
}

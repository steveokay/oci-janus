/**
 * AppShell — the authenticated app frame. Sidebar on the left, topbar on
 * top of the content slot, scrolling main, footer at the bottom.
 *
 * Two motion touches:
 *   * Page transitions — `<motion.div key={pathname}>` fades the main
 *     content area on route change. ~180ms ease-out.
 *   * Background texture — `bg-textured` adds a 2%-opacity SVG noise
 *     over the cream / dark surface so the page has tactile grain.
 *
 * The Cmd+K palette + shortcut listener mount here so they're available
 * across every authenticated route.
 */
import type { ReactNode } from 'react'
import { motion } from 'framer-motion'
import { useLocation } from '@tanstack/react-router'
import { Sidebar } from './Sidebar'
import { Topbar } from './Topbar'
import { Footer } from './Footer'
import {
  CommandPalette,
  useCommandPaletteShortcut,
} from '@/components/palette/CommandPalette'
import { fadeIn } from '@/lib/motion'

export function AppShell({ children }: { children: ReactNode }) {
  useCommandPaletteShortcut()
  const { pathname } = useLocation()

  return (
    <div className="min-h-screen flex bg-surface-muted bg-textured">
      <Sidebar />
      <div className="flex-1 flex flex-col min-w-0">
        <Topbar />
        <main className="flex-1 overflow-y-auto">
          <motion.div
            key={pathname}
            variants={fadeIn}
            initial="initial"
            animate="animate"
          >
            {children}
          </motion.div>
        </main>
        <Footer />
      </div>
      <CommandPalette />
    </div>
  )
}

/**
 * lastVisitedRepoStore — tracks the most recent repo the user opened.
 *
 * Used by the dashboard hero's "Pick up where you left off" link.
 * Persisted to `localStorage` (same rationale as pinnedReposStore — a
 * UI preference, not a credential).
 */
import { create } from 'zustand'

export interface LastVisited {
  org: string
  repo: string
  at: number
}

interface LastVisitedState {
  last: LastVisited | null
  record: (org: string, repo: string) => void
}

const STORAGE_KEY = 'janus.lastVisitedRepo'

function safeRead(): LastVisited | null {
  if (typeof window === 'undefined') return null
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY)
    if (!raw) return null
    const parsed = JSON.parse(raw) as unknown
    if (
      typeof parsed === 'object' &&
      parsed !== null &&
      typeof (parsed as LastVisited).org === 'string' &&
      typeof (parsed as LastVisited).repo === 'string'
    ) {
      return parsed as LastVisited
    }
    return null
  } catch {
    return null
  }
}

export const useLastVisitedRepoStore = create<LastVisitedState>((set) => ({
  last: safeRead(),
  record: (org, repo) => {
    const next: LastVisited = { org, repo, at: Date.now() }
    if (typeof window !== 'undefined') {
      try {
        window.localStorage.setItem(STORAGE_KEY, JSON.stringify(next))
      } catch {
        /* private mode etc. */
      }
    }
    set({ last: next })
  },
}))

/**
 * pinnedReposStore — user-controlled list of "favourite" repos shown
 * on the dashboard.
 *
 * Persisted to `localStorage` under `janus.pinnedRepos`. This is a
 * pure UI preference (no security implication if XSS leaks it — the
 * names are already visible to the user via /repositories). The real
 * fix is a `user_preferences` row on the backend; that ships when the
 * auth service exposes `GET /users/me/preferences` (will be tracked
 * once we get there).
 *
 * Shape: an ordered array of `{ org, repo }` so we can preserve the
 * user's pin order across sessions. The dashboard PinnedRepos panel
 * looks each one up via `useRepository(org, repo)` so the cache is
 * shared with the detail page.
 */
import { create } from 'zustand'

export interface PinnedRepo {
  org: string
  repo: string
}

interface PinnedReposState {
  pinned: PinnedRepo[]
  pin: (target: PinnedRepo) => void
  unpin: (target: PinnedRepo) => void
  isPinned: (target: PinnedRepo) => boolean
}

const STORAGE_KEY = 'janus.pinnedRepos'

function safeRead(): PinnedRepo[] {
  if (typeof window === 'undefined') return []
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY)
    if (!raw) return []
    const parsed = JSON.parse(raw) as unknown
    if (!Array.isArray(parsed)) return []
    // Validate each entry — drop anything malformed so a bad write
    // can't poison the dashboard.
    return parsed.filter(
      (p): p is PinnedRepo =>
        typeof p === 'object' &&
        p !== null &&
        typeof (p as PinnedRepo).org === 'string' &&
        typeof (p as PinnedRepo).repo === 'string',
    )
  } catch {
    return []
  }
}

function safeWrite(pinned: PinnedRepo[]): void {
  if (typeof window === 'undefined') return
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(pinned))
  } catch {
    // Quota / private-mode etc. — silent failure is fine, the user
    // just doesn't get persistence across reloads.
  }
}

const same = (a: PinnedRepo, b: PinnedRepo) =>
  a.org === b.org && a.repo === b.repo

export const usePinnedReposStore = create<PinnedReposState>((set, get) => ({
  pinned: safeRead(),
  pin: (target) => {
    const current = get().pinned
    if (current.some((p) => same(p, target))) return
    const next = [...current, target]
    safeWrite(next)
    set({ pinned: next })
  },
  unpin: (target) => {
    const next = get().pinned.filter((p) => !same(p, target))
    safeWrite(next)
    set({ pinned: next })
  },
  isPinned: (target) => get().pinned.some((p) => same(p, target)),
}))

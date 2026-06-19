/**
 * paletteStore — global open/close state for the command palette.
 *
 * Why Zustand rather than a context: the Cmd+K shortcut is registered
 * at the AppShell level but the topbar's search-styled button also
 * needs to toggle the same dialog. A tiny store is cleaner than a
 * context + provider just to share `open` / `setOpen`.
 */
import { create } from 'zustand'

interface PaletteState {
  open: boolean
  setOpen: (open: boolean) => void
  toggle: () => void
}

export const usePaletteStore = create<PaletteState>((set) => ({
  open: false,
  setOpen: (open) => set({ open }),
  toggle: () => set((s) => ({ open: !s.open })),
}))

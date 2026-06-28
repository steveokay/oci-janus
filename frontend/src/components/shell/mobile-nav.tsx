// REDESIGN-001 Phase 4.6 Task B — mobile off-canvas nav drawer.
//
// Below the `lg` breakpoint the desktop <Sidebar> is `hidden`; this
// component fills the gap. It's a Radix Dialog that slides in from the
// left and renders the shared <SidebarBody> inside. Radix Dialog gives
// us focus-trap, ESC-to-close, outside-click-to-close, and the correct
// ARIA roles for free, so we don't have to roll any of that ourselves.
//
// The drawer is controlled by the parent (Topbar's hamburger button)
// via `open` + `onOpenChange`. SidebarBody is mounted with `onNavigate
// = () => onOpenChange(false)` so tapping a link dismisses the drawer
// in one tap (no need to also tap the backdrop after navigation).
import * as React from "react";
import * as Dialog from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import { SidebarBody } from "./sidebar";
import { cn } from "@/lib/utils";

interface MobileNavProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export function MobileNav({
  open,
  onOpenChange,
}: MobileNavProps): React.ReactElement {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        {/* Backdrop — Radix closes on click; we don't need an onClick handler. */}
        <Dialog.Overlay
          className={cn(
            "fixed inset-0 z-40 bg-black/40 backdrop-blur-sm",
            "data-[state=open]:animate-in data-[state=open]:fade-in-0",
            "data-[state=closed]:animate-out data-[state=closed]:fade-out-0",
          )}
        />
        <Dialog.Content
          // Slides in from the left. The aside-style colour matches the
          // desktop sidebar so the visual continuity is preserved.
          // We DO NOT add lg:hidden — the parent (Topbar) won't render
          // the trigger above lg, so the drawer can never open there.
          className={cn(
            "fixed inset-y-0 left-0 z-50 flex w-[280px] flex-col",
            "border-r border-[var(--color-border)] bg-[var(--color-surface-2)]",
            "shadow-[var(--shadow-floating)]",
            "data-[state=open]:animate-in data-[state=open]:slide-in-from-left",
            "data-[state=closed]:animate-out data-[state=closed]:slide-out-to-left",
            "duration-200",
          )}
          // a11y: Radix injects role=dialog + aria-modal automatically.
          // We provide a title for screen readers (visually hidden).
          aria-label="Workspace navigation"
        >
          {/* VisuallyHidden title satisfies the Radix requirement that
              every Dialog has an accessible name without showing it. */}
          <Dialog.Title className="sr-only">Navigation</Dialog.Title>
          <Dialog.Description className="sr-only">
            Workspace sidebar — sections, settings, and footer
          </Dialog.Description>

          {/* Close button — top-right corner. Radix wires the
              click-to-close and ESC handlers; this is just the visible
              affordance. */}
          <Dialog.Close
            className={cn(
              "absolute right-3 top-3 z-10 rounded-md p-1.5",
              "text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-sunken)]",
              "focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/40",
            )}
            aria-label="Close navigation"
          >
            <X className="size-4" />
          </Dialog.Close>

          <SidebarBody onNavigate={() => onOpenChange(false)} />
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

import * as React from "react";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import { cn } from "@/lib/utils";

export const Dialog = DialogPrimitive.Root;
export const DialogTrigger = DialogPrimitive.Trigger;
export const DialogPortal = DialogPrimitive.Portal;
export const DialogClose = DialogPrimitive.Close;

const Overlay = React.forwardRef<
  React.ElementRef<typeof DialogPrimitive.Overlay>,
  React.ComponentPropsWithoutRef<typeof DialogPrimitive.Overlay>
>(function Overlay({ className, ...props }, ref) {
  return (
    <DialogPrimitive.Overlay
      ref={ref}
      data-beacon-overlay=""
      className={cn(
        "fixed inset-0 z-40 bg-[var(--color-fg)]/35 backdrop-blur-sm",
        className,
      )}
      {...props}
    />
  );
});
export { Overlay as DialogOverlay };

export const DialogContent = React.forwardRef<
  React.ElementRef<typeof DialogPrimitive.Content>,
  React.ComponentPropsWithoutRef<typeof DialogPrimitive.Content>
>(function DialogContent({ className, children, ...props }, ref) {
  return (
    <DialogPortal>
      <Overlay />
      <DialogPrimitive.Content
        ref={ref}
        data-beacon-content=""
        // 2026-07-01 (user report): tall dialogs (Create Webhook, Create
        // Trust, etc.) overflowed the viewport and clipped the footer
        // Save button below the fold. The outer container now caps at
        // 100dvh - 2rem and lays out as a flex column so the inner
        // scroll region can take flex-1 + overflow-y-auto. The close
        // button stays absolute on the OUTER container so it doesn't
        // scroll with the inner children — visible even at scroll
        // position N. Existing consumers don't need to change; they
        // just get scrollable content for free.
        className={cn(
          "fixed left-1/2 top-1/2 z-50 w-full max-w-[480px] -translate-x-1/2 -translate-y-1/2",
          "flex max-h-[calc(100dvh-2rem)] flex-col",
          "rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)]",
          "shadow-[var(--shadow-floating)]",
          className,
        )}
        {...props}
      >
        <div className="flex-1 overflow-y-auto overscroll-contain p-6">
          {children}
        </div>
        <DialogPrimitive.Close
          className={cn(
            "absolute right-4 top-4 rounded-md p-1 text-[var(--color-fg-muted)]",
            // Ensure the close button sits above the scrollable inner
            // region (which uses relative positioning implicitly). z-10
            // is enough to escape without competing with the outer z-50.
            "z-10 bg-[var(--color-surface)]",
            "transition-colors hover:bg-[var(--color-surface-sunken)] hover:text-[var(--color-fg)]",
            // Visible focus ring (DSGN-017). Offset 1 because the button sits
            // tight against the dialog corner; a wider offset would clip.
            "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)] focus-visible:ring-offset-1 focus-visible:ring-offset-[var(--color-surface)]",
          )}
          aria-label="Close"
        >
          <X className="size-4" />
        </DialogPrimitive.Close>
      </DialogPrimitive.Content>
    </DialogPortal>
  );
});

export function DialogHeader({
  className,
  ...props
}: React.HTMLAttributes<HTMLDivElement>): React.ReactElement {
  return (
    <div
      className={cn("mb-4 flex flex-col gap-1.5 pr-8", className)}
      {...props}
    />
  );
}

export const DialogTitle = React.forwardRef<
  React.ElementRef<typeof DialogPrimitive.Title>,
  React.ComponentPropsWithoutRef<typeof DialogPrimitive.Title>
>(function DialogTitle({ className, ...props }, ref) {
  return (
    <DialogPrimitive.Title
      ref={ref}
      className={cn(
        "text-lg font-semibold leading-tight tracking-tight",
        className,
      )}
      {...props}
    />
  );
});

export const DialogDescription = React.forwardRef<
  React.ElementRef<typeof DialogPrimitive.Description>,
  React.ComponentPropsWithoutRef<typeof DialogPrimitive.Description>
>(function DialogDescription({ className, ...props }, ref) {
  return (
    <DialogPrimitive.Description
      ref={ref}
      className={cn("text-sm text-[var(--color-fg-muted)]", className)}
      {...props}
    />
  );
});

export function DialogFooter({
  className,
  ...props
}: React.HTMLAttributes<HTMLDivElement>): React.ReactElement {
  return (
    <div
      className={cn(
        "mt-6 flex flex-col-reverse gap-2 sm:flex-row sm:justify-end",
        className,
      )}
      {...props}
    />
  );
}

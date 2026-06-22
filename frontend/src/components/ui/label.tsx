import * as React from "react";
import * as LabelPrimitive from "@radix-ui/react-label";
import { cn } from "@/lib/utils";

export const Label = React.forwardRef<
  React.ElementRef<typeof LabelPrimitive.Root>,
  React.ComponentPropsWithoutRef<typeof LabelPrimitive.Root>
>(function Label({ className, ...props }, ref) {
  // S-MAINT-1 B4: `block mb-1.5` default so Label always has a 6px gap to its
  // sibling input. Dialogs that wrap fields in `space-y-1.5` collapse this
  // (vertical block margins take the max) so well-structured callers are
  // unaffected; the dialogs that omit the wrapper used to render with the
  // Label visually touching the Input — that's the bug. Explicit `mb-*`
  // / `mt-*` overrides on individual callers still win via twMerge.
  return (
    <LabelPrimitive.Root
      ref={ref}
      className={cn(
        "block mb-1.5 text-xs font-medium uppercase tracking-wider text-[var(--color-fg-muted)]",
        "peer-disabled:cursor-not-allowed peer-disabled:opacity-50",
        className,
      )}
      {...props}
    />
  );
});

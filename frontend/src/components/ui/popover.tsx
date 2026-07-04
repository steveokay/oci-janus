import * as React from "react";
import * as PopoverPrimitive from "@radix-ui/react-popover";
import { cn } from "@/lib/utils";

// Beacon — Popover primitive. Thin wrapper around @radix-ui/react-popover,
// styled to match the floating-surface look of the Dialog primitive.
//
// Use this (not DropdownMenu) whenever the floating panel contains a
// free-form region of links/buttons/scrollable content rather than a list
// of single-action menu items. DropdownMenu imposes menu semantics
// (role="menu", roving tabindex, typeahead) that are wrong for a rich
// panel and break normal Tab/arrow navigation of the contents.

export const Popover = PopoverPrimitive.Root;
export const PopoverTrigger = PopoverPrimitive.Trigger;
export const PopoverAnchor = PopoverPrimitive.Anchor;
export const PopoverClose = PopoverPrimitive.Close;

export const PopoverContent = React.forwardRef<
  React.ElementRef<typeof PopoverPrimitive.Content>,
  React.ComponentPropsWithoutRef<typeof PopoverPrimitive.Content>
>(function PopoverContent(
  { className, align = "center", sideOffset = 6, ...props },
  ref,
) {
  return (
    <PopoverPrimitive.Portal>
      <PopoverPrimitive.Content
        ref={ref}
        align={align}
        sideOffset={sideOffset}
        // Same floating-surface treatment as DialogContent so popovers and
        // dialogs read as the same elevation layer. Radix handles the
        // outside-click + ESC dismissal for free.
        className={cn(
          "z-50 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-2)]",
          "shadow-[var(--shadow-floating)] outline-none",
          className,
        )}
        {...props}
      />
    </PopoverPrimitive.Portal>
  );
});

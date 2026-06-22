import * as React from "react";
import * as SelectPrimitive from "@radix-ui/react-select";
import { Check, ChevronDown } from "lucide-react";
import { cn } from "@/lib/utils";

// Beacon — Select primitive.
//
// Thin wrappers over @radix-ui/react-select that hard-wire the Beacon design
// tokens so callers get a styled dropdown matching dialogs, tabs and the rest
// of the design system. Mirrors the shadcn-ui surface API so refactors from
// the native `<select>` are mechanical.
//
// Why we don't use the native element: the native `<select>` respects the OS
// theme — light gray on macOS / blue accent on Windows — which jarringly
// clashes with the Beacon palette inside an otherwise-themed surface. The
// admin SA-picker on /api-keys/activity is the first concrete pain point; the
// stale `// TODO (FUT-005)` in that file is closed by adopting this primitive.

export const Select = SelectPrimitive.Root;
export const SelectGroup = SelectPrimitive.Group;
export const SelectValue = SelectPrimitive.Value;

// SelectTrigger — the clickable closed-state of the dropdown.
// Visual: looks like an Input (same border + bg + radius) with a chevron
// affordance on the right. Active focus ring uses the accent token so it
// matches input focus elsewhere.
export const SelectTrigger = React.forwardRef<
  React.ElementRef<typeof SelectPrimitive.Trigger>,
  React.ComponentPropsWithoutRef<typeof SelectPrimitive.Trigger>
>(function SelectTrigger({ className, children, ...props }, ref) {
  return (
    <SelectPrimitive.Trigger
      ref={ref}
      className={cn(
        "inline-flex h-8 items-center justify-between gap-2 rounded-md border",
        "border-[var(--color-border)] bg-[var(--color-surface)]",
        "px-2.5 py-1 text-sm text-[var(--color-fg)]",
        "transition-colors",
        "hover:bg-[var(--color-surface-sunken)]",
        "focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)] focus-visible:ring-offset-1 focus-visible:ring-offset-[var(--color-bg)]",
        "data-[state=open]:border-[var(--color-accent-border)]",
        "data-[placeholder]:text-[var(--color-fg-muted)]",
        "disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    >
      {children}
      <SelectPrimitive.Icon asChild>
        <ChevronDown
          aria-hidden
          className="size-3.5 shrink-0 text-[var(--color-fg-subtle)] transition-transform data-[state=open]:rotate-180"
        />
      </SelectPrimitive.Icon>
    </SelectPrimitive.Trigger>
  );
});

// SelectContent — the popover wrapper. Portalled to document.body so it can
// escape parent overflow:hidden containers (drawers, dialogs).
export const SelectContent = React.forwardRef<
  React.ElementRef<typeof SelectPrimitive.Content>,
  React.ComponentPropsWithoutRef<typeof SelectPrimitive.Content>
>(function SelectContent(
  { className, children, position = "popper", sideOffset = 4, ...props },
  ref,
) {
  return (
    <SelectPrimitive.Portal>
      <SelectPrimitive.Content
        ref={ref}
        position={position}
        sideOffset={sideOffset}
        className={cn(
          "z-50 min-w-[var(--radix-select-trigger-width)] overflow-hidden rounded-md border",
          "border-[var(--color-border)] bg-[var(--color-surface)]",
          "shadow-[0_4px_18px_-6px_rgba(0,0,0,0.18)]",
          // No keyframe animation — the project doesn't use tailwindcss-animate;
          // the chevron rotation + border-color change on the trigger already
          // signal state change.
          className,
        )}
        {...props}
      >
        <SelectPrimitive.Viewport className="p-1">
          {children}
        </SelectPrimitive.Viewport>
      </SelectPrimitive.Content>
    </SelectPrimitive.Portal>
  );
});

// SelectLabel — used inside a <SelectGroup> to caption a section of options
// (e.g. "Service accounts"). Visually matches the small-caps style used by
// the rest of the app (AccessSubNav rail group labels, repo settings, etc.).
export const SelectLabel = React.forwardRef<
  React.ElementRef<typeof SelectPrimitive.Label>,
  React.ComponentPropsWithoutRef<typeof SelectPrimitive.Label>
>(function SelectLabel({ className, ...props }, ref) {
  return (
    <SelectPrimitive.Label
      ref={ref}
      className={cn(
        "px-2 pb-1 pt-2 text-[10px] font-medium uppercase tracking-[0.14em]",
        "text-[var(--color-fg-subtle)]",
        className,
      )}
      {...props}
    />
  );
});

// SelectItem — one option row inside the dropdown.
// Includes a left-aligned check indicator that appears when the item is the
// selected value. Right-aligned content (e.g. shortcut hints) can be passed
// as children after the label text.
export const SelectItem = React.forwardRef<
  React.ElementRef<typeof SelectPrimitive.Item>,
  React.ComponentPropsWithoutRef<typeof SelectPrimitive.Item>
>(function SelectItem({ className, children, ...props }, ref) {
  return (
    <SelectPrimitive.Item
      ref={ref}
      className={cn(
        "relative flex w-full cursor-pointer select-none items-center gap-2 rounded-sm",
        "py-1.5 pl-7 pr-2 text-sm text-[var(--color-fg)]",
        "outline-none transition-colors",
        "data-[highlighted]:bg-[var(--color-surface-sunken)] data-[highlighted]:text-[var(--color-fg)]",
        "data-[state=checked]:font-medium",
        "data-[disabled]:pointer-events-none data-[disabled]:opacity-50",
        className,
      )}
      {...props}
    >
      <span className="absolute left-2 flex size-3.5 items-center justify-center">
        <SelectPrimitive.ItemIndicator>
          <Check className="size-3 text-[var(--color-accent)]" />
        </SelectPrimitive.ItemIndicator>
      </span>
      <SelectPrimitive.ItemText>{children}</SelectPrimitive.ItemText>
    </SelectPrimitive.Item>
  );
});

// SelectSeparator — horizontal divider between groups. Rarely used but the
// shadcn surface ships it, so keep it available for forward-compat.
export const SelectSeparator = React.forwardRef<
  React.ElementRef<typeof SelectPrimitive.Separator>,
  React.ComponentPropsWithoutRef<typeof SelectPrimitive.Separator>
>(function SelectSeparator({ className, ...props }, ref) {
  return (
    <SelectPrimitive.Separator
      ref={ref}
      className={cn("my-1 h-px bg-[var(--color-border)]", className)}
      {...props}
    />
  );
});

import * as React from "react";
import { cn } from "@/lib/utils";

export type InputProps = React.InputHTMLAttributes<HTMLInputElement>;

// Beacon — Input. Visible 1px border with stronger contrast on focus.
// Avoid an outline ring on top of the focus shadow so the field doesn't
// "double-ring" the user.
export const Input = React.forwardRef<HTMLInputElement, InputProps>(
  function Input({ className, ...props }, ref) {
    return (
      <input
        ref={ref}
        className={cn(
          "flex h-10 w-full rounded-md border border-[var(--color-border-strong)]",
          "bg-[var(--color-surface)] px-3 py-2 text-sm",
          "placeholder:text-[var(--color-fg-subtle)]",
          "focus-visible:outline-none focus-visible:border-[var(--color-accent)]",
          "disabled:cursor-not-allowed disabled:opacity-50",
          "transition-colors",
          className,
        )}
        {...props}
      />
    );
  },
);

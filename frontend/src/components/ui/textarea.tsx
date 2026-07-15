import * as React from "react";
import { cn } from "@/lib/utils";

export type TextareaProps = React.TextareaHTMLAttributes<HTMLTextAreaElement>;

// Beacon — Textarea. Multi-line sibling of Input; shares the same border,
// focus, and disabled treatment so long-form fields (repo README, etc.) sit
// visually consistent with single-line inputs. Vertical resize only — a
// horizontal drag would break the surrounding card layout.
export const Textarea = React.forwardRef<HTMLTextAreaElement, TextareaProps>(
  function Textarea({ className, ...props }, ref) {
    return (
      <textarea
        ref={ref}
        className={cn(
          "flex min-h-[6rem] w-full rounded-md border border-[var(--color-border-strong)]",
          "bg-[var(--color-surface)] px-3 py-2 text-sm",
          "placeholder:text-[var(--color-fg-subtle)]",
          "focus-visible:outline-none focus-visible:border-[var(--color-accent)]",
          "disabled:cursor-not-allowed disabled:opacity-50",
          "resize-y transition-colors",
          className,
        )}
        {...props}
      />
    );
  },
);

import * as React from "react";
import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

// Beacon — Button. Five variants keyed to the semantic token system.
// `accent` is the workhorse (teal). `highlight` is the warm-amber CTA used
// sparingly for live/urgent actions. `outline` and `ghost` are for secondary
// affordances; `danger` for destructive flows.
const buttonVariants = cva(
  [
    "inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-md",
    "text-sm font-medium tracking-tight transition-[background-color,box-shadow,color] duration-150",
    "disabled:pointer-events-none disabled:opacity-50",
    // Visible focus ring for keyboard / screen-reader users (DSGN-017, WCAG 2.4.7).
    "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--color-bg)]",
    "[&_svg]:size-4 [&_svg]:shrink-0",
  ].join(" "),
  {
    variants: {
      variant: {
        accent:
          "bg-[var(--color-accent)] text-[var(--color-accent-fg)] hover:bg-[var(--color-accent-hover)]",
        highlight:
          "bg-[var(--color-highlight)] text-white hover:brightness-110",
        outline:
          "border border-[var(--color-border-strong)] bg-transparent text-[var(--color-fg)] hover:bg-[var(--color-surface-sunken)]",
        ghost:
          "bg-transparent text-[var(--color-fg)] hover:bg-[var(--color-surface-sunken)]",
        danger:
          "bg-[var(--color-danger)] text-white hover:brightness-110",
        link:
          "text-[var(--color-accent)] underline-offset-4 hover:underline",
      },
      size: {
        sm: "h-8 px-3 text-xs",
        md: "h-10 px-4",
        lg: "h-11 px-5 text-base",
        icon: "h-9 w-9",
      },
    },
    defaultVariants: { variant: "accent", size: "md" },
  },
);

export interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {
  asChild?: boolean;
  loading?: boolean;
}

export const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  function Button(
    { className, variant, size, asChild = false, loading, children, ...props },
    ref,
  ) {
    const classes = cn(buttonVariants({ variant, size, className }));

    // asChild path uses Radix Slot to forward props to a single child
    // (typically a <Link>). Newer Radix versions (>=1.1) reject multi-child
    // JSX even when one is null, so we render Slot WITHOUT the spinner —
    // asChild + loading was never meaningful anyway (the slotted child
    // isn't a real <button>).
    if (asChild) {
      return (
        <Slot
          ref={ref}
          className={classes}
          aria-busy={loading || undefined}
          {...props}
        >
          {children}
        </Slot>
      );
    }

    return (
      <button
        ref={ref}
        className={classes}
        aria-busy={loading || undefined}
        {...props}
      >
        {loading ? (
          // Inline spinner — circle with the conic-gradient trick rendered
          // entirely from a single span so the button doesn't need an svg.
          <span
            className="inline-block size-4 animate-spin rounded-full border-2 border-current border-t-transparent"
            aria-hidden
          />
        ) : null}
        {children}
      </button>
    );
  },
);

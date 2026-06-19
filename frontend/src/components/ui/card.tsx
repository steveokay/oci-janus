import * as React from "react";
import { cn } from "@/lib/utils";

// Beacon — Card. The default surface for grouped content.
// The optional `accentBar` paints a 2px colored stripe along the top edge —
// the "status-keyed top border" we promised in the design direction.
type AccentTone = "neutral" | "accent" | "success" | "warning" | "danger";

const accentToneClass: Record<AccentTone, string> = {
  neutral: "bg-[var(--color-border)]",
  accent: "bg-[var(--color-accent)]",
  success: "bg-[var(--color-success)]",
  warning: "bg-[var(--color-warning)]",
  danger: "bg-[var(--color-danger)]",
};

interface CardProps extends React.HTMLAttributes<HTMLDivElement> {
  accentBar?: AccentTone;
}

export const Card = React.forwardRef<HTMLDivElement, CardProps>(function Card(
  { className, accentBar, children, ...props },
  ref,
) {
  return (
    <div
      ref={ref}
      className={cn(
        "relative overflow-hidden rounded-lg border border-[var(--color-border)]",
        "bg-[var(--color-surface)] text-[var(--color-fg)]",
        "shadow-[var(--shadow-card)]",
        className,
      )}
      {...props}
    >
      {accentBar ? (
        <span
          aria-hidden
          className={cn(
            "absolute inset-x-0 top-0 h-[2px]",
            accentToneClass[accentBar],
          )}
        />
      ) : null}
      {children}
    </div>
  );
});

export const CardHeader = React.forwardRef<
  HTMLDivElement,
  React.HTMLAttributes<HTMLDivElement>
>(function CardHeader({ className, ...props }, ref) {
  return (
    <div
      ref={ref}
      className={cn("flex flex-col gap-1 p-6 pb-3", className)}
      {...props}
    />
  );
});

export const CardTitle = React.forwardRef<
  HTMLHeadingElement,
  React.HTMLAttributes<HTMLHeadingElement>
>(function CardTitle({ className, ...props }, ref) {
  return (
    <h3
      ref={ref}
      className={cn(
        "text-base font-semibold leading-tight tracking-tight text-[var(--color-fg)]",
        className,
      )}
      {...props}
    />
  );
});

export const CardDescription = React.forwardRef<
  HTMLParagraphElement,
  React.HTMLAttributes<HTMLParagraphElement>
>(function CardDescription({ className, ...props }, ref) {
  return (
    <p
      ref={ref}
      className={cn("text-sm text-[var(--color-fg-muted)]", className)}
      {...props}
    />
  );
});

export const CardContent = React.forwardRef<
  HTMLDivElement,
  React.HTMLAttributes<HTMLDivElement>
>(function CardContent({ className, ...props }, ref) {
  return (
    <div ref={ref} className={cn("p-6 pt-3", className)} {...props} />
  );
});

export const CardFooter = React.forwardRef<
  HTMLDivElement,
  React.HTMLAttributes<HTMLDivElement>
>(function CardFooter({ className, ...props }, ref) {
  return (
    <div
      ref={ref}
      className={cn(
        "flex items-center gap-2 border-t border-[var(--color-border)] p-4",
        className,
      )}
      {...props}
    />
  );
});

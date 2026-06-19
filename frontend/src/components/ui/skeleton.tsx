import * as React from "react";
import { cn } from "@/lib/utils";

// Beacon — Skeleton. The shimmer animation lives in index.css so callers
// only choose width/height/shape.
export function Skeleton({
  className,
  ...props
}: React.HTMLAttributes<HTMLDivElement>): React.ReactElement {
  return (
    <div
      className={cn("skeleton-shimmer rounded-md", className)}
      aria-hidden
      {...props}
    />
  );
}

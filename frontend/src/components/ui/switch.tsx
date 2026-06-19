import * as React from "react";
import * as SwitchPrimitives from "@radix-ui/react-switch";
import { cn } from "@/lib/utils";

export const Switch = React.forwardRef<
  React.ElementRef<typeof SwitchPrimitives.Root>,
  React.ComponentPropsWithoutRef<typeof SwitchPrimitives.Root>
>(function Switch({ className, ...props }, ref) {
  return (
    <SwitchPrimitives.Root
      ref={ref}
      className={cn(
        "peer inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full",
        "border border-transparent transition-colors",
        "focus-visible:outline-none",
        "data-[state=checked]:bg-[var(--color-accent)]",
        "data-[state=unchecked]:bg-[var(--color-border-strong)]",
        "disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    >
      <SwitchPrimitives.Thumb
        className={cn(
          "pointer-events-none block size-4 rounded-full bg-white shadow-sm ring-0 transition-transform",
          "data-[state=checked]:translate-x-[18px] data-[state=unchecked]:translate-x-0.5",
        )}
      />
    </SwitchPrimitives.Root>
  );
});

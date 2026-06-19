import * as React from "react";
import * as TabsPrimitive from "@radix-ui/react-tabs";
import { cn } from "@/lib/utils";

export const Tabs = TabsPrimitive.Root;

export const TabsList = React.forwardRef<
  React.ElementRef<typeof TabsPrimitive.List>,
  React.ComponentPropsWithoutRef<typeof TabsPrimitive.List>
>(function TabsList({ className, ...props }, ref) {
  return (
    <TabsPrimitive.List
      ref={ref}
      className={cn(
        "inline-flex h-10 items-center gap-1 border-b border-[var(--color-border)]",
        className,
      )}
      {...props}
    />
  );
});

export const TabsTrigger = React.forwardRef<
  React.ElementRef<typeof TabsPrimitive.Trigger>,
  React.ComponentPropsWithoutRef<typeof TabsPrimitive.Trigger>
>(function TabsTrigger({ className, ...props }, ref) {
  return (
    <TabsPrimitive.Trigger
      ref={ref}
      className={cn(
        "relative inline-flex h-10 items-center gap-2 rounded-sm px-3 text-sm font-medium",
        "text-[var(--color-fg-muted)] transition-colors",
        "hover:text-[var(--color-fg)] focus-visible:outline-none",
        "data-[state=active]:text-[var(--color-fg)]",
        // The active state paints a 2px underline that aligns with the bottom
        // border of TabsList — gives the tab a precise selection cue without
        // a heavyweight pill background.
        "data-[state=active]:after:absolute data-[state=active]:after:inset-x-2 data-[state=active]:after:-bottom-px",
        "data-[state=active]:after:h-[2px] data-[state=active]:after:rounded-full",
        "data-[state=active]:after:bg-[var(--color-accent)]",
        className,
      )}
      {...props}
    />
  );
});

export const TabsContent = React.forwardRef<
  React.ElementRef<typeof TabsPrimitive.Content>,
  React.ComponentPropsWithoutRef<typeof TabsPrimitive.Content>
>(function TabsContent({ className, ...props }, ref) {
  return (
    <TabsPrimitive.Content
      ref={ref}
      className={cn("mt-6 focus-visible:outline-none", className)}
      {...props}
    />
  );
});

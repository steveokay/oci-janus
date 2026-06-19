import * as React from "react";
import { cn } from "@/lib/utils";

// Beacon — Table primitives. Plain semantic HTML wrapped in styled shells
// so any table call site reads the same density + hover behaviour. We do
// NOT pull in a heavy table library here; the surfaces in this app are list
// renders, not spreadsheet grids.

export const Table = React.forwardRef<
  HTMLTableElement,
  React.HTMLAttributes<HTMLTableElement>
>(function Table({ className, ...props }, ref) {
  return (
    <div className="relative w-full overflow-x-auto">
      <table
        ref={ref}
        className={cn("w-full caption-bottom text-sm", className)}
        {...props}
      />
    </div>
  );
});

export const TableHeader = React.forwardRef<
  HTMLTableSectionElement,
  React.HTMLAttributes<HTMLTableSectionElement>
>(function TableHeader({ className, ...props }, ref) {
  return (
    <thead
      ref={ref}
      className={cn(
        "border-b border-[var(--color-border)] bg-[var(--color-surface-sunken)]",
        className,
      )}
      {...props}
    />
  );
});

export const TableBody = React.forwardRef<
  HTMLTableSectionElement,
  React.HTMLAttributes<HTMLTableSectionElement>
>(function TableBody({ className, ...props }, ref) {
  return (
    <tbody
      ref={ref}
      className={cn("[&_tr:last-child]:border-0", className)}
      {...props}
    />
  );
});

interface TableRowProps extends React.HTMLAttributes<HTMLTableRowElement> {
  interactive?: boolean;
}

// `interactive` rows get the left-accent hover state — used wherever clicking
// a row drills into a detail view.
export const TableRow = React.forwardRef<HTMLTableRowElement, TableRowProps>(
  function TableRow({ className, interactive, ...props }, ref) {
    return (
      <tr
        ref={ref}
        className={cn(
          "border-b border-[var(--color-border)] transition-colors",
          interactive &&
            "cursor-pointer relative hover:bg-[var(--color-surface-sunken)] focus-within:bg-[var(--color-surface-sunken)]",
          interactive &&
            "before:absolute before:left-0 before:top-0 before:h-full before:w-[2px] before:bg-transparent hover:before:bg-[var(--color-accent)]",
          className,
        )}
        {...props}
      />
    );
  },
);

export const TableHead = React.forwardRef<
  HTMLTableCellElement,
  React.ThHTMLAttributes<HTMLTableCellElement>
>(function TableHead({ className, ...props }, ref) {
  return (
    <th
      ref={ref}
      className={cn(
        "h-10 px-4 text-left align-middle",
        "text-[11px] font-medium uppercase tracking-[0.14em] text-[var(--color-fg-subtle)]",
        className,
      )}
      {...props}
    />
  );
});

export const TableCell = React.forwardRef<
  HTMLTableCellElement,
  React.TdHTMLAttributes<HTMLTableCellElement>
>(function TableCell({ className, ...props }, ref) {
  return (
    <td
      ref={ref}
      className={cn("h-12 px-4 align-middle", className)}
      {...props}
    />
  );
});

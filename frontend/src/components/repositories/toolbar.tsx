import * as React from "react";
import { Search, Plus } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import type { RepoVisibilityFilter } from "@/lib/api/repositories";
import { cn } from "@/lib/utils";

interface ToolbarProps {
  query: string;
  onQueryChange: (q: string) => void;
  visibility: RepoVisibilityFilter;
  onVisibilityChange: (v: RepoVisibilityFilter) => void;
  onCreateClick: () => void;
}

const FILTERS: Array<{ value: RepoVisibilityFilter; label: string }> = [
  { value: "all", label: "All" },
  { value: "public", label: "Public" },
  { value: "private", label: "Private" },
];

export function RepositoriesToolbar({
  query,
  onQueryChange,
  visibility,
  onVisibilityChange,
  onCreateClick,
}: ToolbarProps): React.ReactElement {
  return (
    <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      <div className="flex flex-1 items-center gap-3">
        <div className="relative w-full max-w-sm">
          <Search
            className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-[var(--color-fg-subtle)]"
            aria-hidden
          />
          <Input
            className="pl-9"
            type="search"
            placeholder="Filter by name or org…"
            value={query}
            onChange={(e) => onQueryChange(e.target.value)}
            aria-label="Filter repositories"
          />
        </div>

        <div className="hidden items-center gap-1 rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] p-1 md:flex">
          {FILTERS.map((f) => {
            const active = visibility === f.value;
            return (
              <button
                key={f.value}
                type="button"
                onClick={() => onVisibilityChange(f.value)}
                className={cn(
                  "rounded-sm px-3 py-1 text-xs font-medium transition-colors",
                  active
                    ? "bg-[var(--color-surface-sunken)] text-[var(--color-fg)]"
                    : "text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]",
                )}
                aria-pressed={active}
              >
                {f.label}
              </button>
            );
          })}
        </div>
      </div>
      <Button onClick={onCreateClick}>
        <Plus className="size-4" />
        New repository
      </Button>
    </div>
  );
}

import * as React from "react";
import { cn } from "@/lib/utils";

// SectionAnchorNav — a compact horizontal row of anchor "chips" that jump to
// the in-page `<section id="…">` blocks beneath a long settings tab. Unlike
// RepoSettingsToc this is deliberately dumb: no IntersectionObserver /
// scroll-spy, just plain `<a href="#id">` links styled as small muted pills.
// The settings tabs it fronts are short enough that active-highlighting isn't
// worth the observer machinery — the value is purely "here are the sections,
// jump to one". Native anchor navigation also composes with the sections'
// existing `scroll-mt-24` so the target header isn't tucked under the topbar.
//
// Callers pass only the sections that are actually rendered — e.g. the
// workspace tab's single-mode block is conditional, so it filters the item
// list before handing it here rather than rendering dead chips.

interface SectionAnchorItem {
  id: string;
  label: string;
}

interface SectionAnchorNavProps {
  items: SectionAnchorItem[];
  // Accessible name for the nav landmark — each tab supplies its own so AT
  // can tell the platform vs. workspace section rails apart.
  ariaLabel: string;
}

export function SectionAnchorNav({
  items,
  ariaLabel,
}: SectionAnchorNavProps): React.ReactElement | null {
  // Nothing to expose (e.g. multi-mode workspace filtered every chip away):
  // render nothing rather than an empty bar.
  if (items.length === 0) return null;

  return (
    <nav aria-label={ariaLabel} className="flex flex-wrap gap-2">
      {items.map((item) => (
        <a
          key={item.id}
          href={`#${item.id}`}
          className={cn(
            "rounded-full border border-[var(--color-border)] bg-[var(--color-surface-sunken)]",
            "px-3 py-1 text-xs font-medium text-[var(--color-fg-muted)] transition-colors",
            "hover:border-[var(--color-border-strong)] hover:text-[var(--color-fg)]",
            // a11y — visible keyboard focus ring, matching the app's link idiom.
            "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/40",
          )}
        >
          {item.label}
        </a>
      ))}
    </nav>
  );
}

import * as React from "react";
import { cn } from "@/lib/utils";

// RepoSettingsToc — DSGN-006 optional sticky right-side ToC for the
// repo Settings tab. Only rendered at xl: and up so the lg / md / sm
// layouts keep the existing single-column flow.
//
// The active section is computed via IntersectionObserver against the
// matching `<section id="...">` anchors. Using IO (rather than a
// scroll-event listener) keeps the work off the main thread on every
// scroll tick — the observer fires only when an entry crosses the
// configured rootMargin band, so we update React state at most a few
// times per quick scroll instead of on every wheel event.

interface RepoSettingsTocItem {
  id: string;
  label: string;
}

interface RepoSettingsTocProps {
  items: RepoSettingsTocItem[];
}

export function RepoSettingsToc({
  items,
}: RepoSettingsTocProps): React.ReactElement {
  const [activeId, setActiveId] = React.useState<string>(items[0]?.id ?? "");

  React.useEffect(() => {
    if (typeof window === "undefined" || items.length === 0) {
      return;
    }

    const elements = items
      .map((item) => document.getElementById(item.id))
      .filter((el): el is HTMLElement => el !== null);

    if (elements.length === 0) {
      return;
    }

    // rootMargin: shrink the viewport so a section is considered
    // "active" once its top crosses ~20% from the viewport top and
    // until its bottom leaves ~60% from the bottom. This avoids the
    // common ToC bug where the very top of the page never highlights
    // anything because the first section is too tall to be fully
    // intersecting.
    const observer = new IntersectionObserver(
      (entries) => {
        const visible = entries
          .filter((entry) => entry.isIntersecting)
          .sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top);
        if (visible.length > 0 && visible[0].target.id) {
          setActiveId(visible[0].target.id);
        }
      },
      {
        rootMargin: "-20% 0px -60% 0px",
        threshold: 0,
      },
    );

    for (const el of elements) {
      observer.observe(el);
    }

    return () => {
      observer.disconnect();
    };
  }, [items]);

  return (
    <nav
      aria-label="Repository settings sections"
      className="sticky top-24 hidden xl:block"
    >
      <p className="mb-2 text-[10px] font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
        On this page
      </p>
      <ul className="flex flex-col gap-1 border-l border-[var(--color-border)]">
        {items.map((item) => {
          const isActive = item.id === activeId;
          return (
            <li key={item.id}>
              <a
                href={`#${item.id}`}
                className={cn(
                  "block -ml-px border-l-2 px-3 py-1 text-sm transition-colors",
                  isActive
                    ? "border-[var(--color-accent)] text-[var(--color-fg)] font-medium"
                    : "border-transparent text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]",
                )}
              >
                {item.label}
              </a>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}

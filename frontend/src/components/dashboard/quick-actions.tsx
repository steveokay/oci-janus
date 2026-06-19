import * as React from "react";
import { Link } from "@tanstack/react-router";
import {
  ArrowRight,
  Boxes,
  ShieldCheck,
  Webhook,
  KeyRound,
} from "lucide-react";

interface Action {
  to: string;
  label: string;
  description: string;
  icon: React.ComponentType<{ className?: string }>;
}

// Compact "what would you like to do next" list. Avoids cluttering the
// dashboard with a full "command palette" while still surfacing the next
// most-likely operator actions.
const ACTIONS: Action[] = [
  {
    to: "/repositories",
    label: "Browse repositories",
    description: "Review and manage your container images.",
    icon: Boxes,
  },
  {
    to: "/security",
    label: "Inspect vulnerabilities",
    description: "Open scan results across your workspace.",
    icon: ShieldCheck,
  },
  {
    to: "/webhooks",
    label: "Wire a webhook",
    description: "Stream registry events to your CI / on-call stack.",
    icon: Webhook,
  },
  {
    to: "/api-keys",
    label: "Issue an API key",
    description: "Hand a robot account scoped registry access.",
    icon: KeyRound,
  },
];

export function QuickActions(): React.ReactElement {
  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
      {ACTIONS.map(({ to, label, description, icon: Icon }) => (
        <Link
          key={to}
          to={to}
          className="group flex items-start gap-3 rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4 transition-colors hover:border-[var(--color-accent)] hover:bg-[var(--color-surface-sunken)]"
        >
          <span
            className="mt-0.5 grid size-8 shrink-0 place-items-center rounded-md bg-[var(--color-accent-subtle)] text-[var(--color-accent)]"
            aria-hidden
          >
            <Icon className="size-4" />
          </span>
          <div className="min-w-0 flex-1">
            <div className="flex items-center justify-between gap-2">
              <span className="text-sm font-medium text-[var(--color-fg)]">
                {label}
              </span>
              <ArrowRight className="size-4 shrink-0 -translate-x-1 text-[var(--color-fg-subtle)] transition-transform group-hover:translate-x-0 group-hover:text-[var(--color-accent)]" />
            </div>
            <p className="mt-1 text-xs text-[var(--color-fg-muted)]">
              {description}
            </p>
          </div>
        </Link>
      ))}
    </div>
  );
}

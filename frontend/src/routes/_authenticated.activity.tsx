import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { Activity, ArrowUpCircle, Webhook, ShieldCheck, KeyRound } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";

export const Route = createFileRoute("/_authenticated/activity")({
  component: ActivityPage,
});

function ActivityPage(): React.ReactElement {
  return (
    <div className="space-y-6">
      <header className="flex flex-col gap-1">
        <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          Audit
        </p>
        <h1 className="font-display text-3xl font-medium tracking-tight">
          Activity
        </h1>
        <p className="text-sm text-[var(--color-fg-muted)]">
          A live feed of what's happening across the workspace — pushes, deletes,
          scans, webhook deliveries, RBAC changes.
        </p>
      </header>

      <Card>
        <CardHeader>
          <div className="flex items-center justify-between">
            <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              Activity stream
            </CardDescription>
            <Badge tone="accent" className="font-mono">
              FE-API-008
            </Badge>
          </div>
        </CardHeader>
        <CardContent className="space-y-5">
          <p className="text-sm text-[var(--color-fg-muted)]">
            The audit service already records every event we'd surface here.
            The pending piece is a poll / SSE endpoint on the management API
            that hands the events back to the UI.
          </p>

          {/* A sketched preview of what this will look like, so reviewers can
              imagine it. Each row is a static placeholder — not a real event. */}
          <div className="rounded-lg border border-dashed border-[var(--color-border)] bg-[var(--color-surface-sunken)] p-4">
            <div className="mb-3 text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              Preview
            </div>
            <ol className="space-y-3">
              <PreviewRow
                icon={<ArrowUpCircle className="size-4" />}
                title="Push completed"
                detail="acme/api:1.4.2 — 412 MB"
                meta="2 minutes ago · ci-bot"
                tone="success"
              />
              <PreviewRow
                icon={<ShieldCheck className="size-4" />}
                title="Scan completed"
                detail="acme/api:1.4.2 — 3 HIGH, 12 MEDIUM"
                meta="2 minutes ago · trivy"
                tone="warning"
              />
              <PreviewRow
                icon={<Webhook className="size-4" />}
                title="Webhook delivered"
                detail="https://hooks.acme.com/registry — 200 OK"
                meta="1 minute ago · image.pushed"
                tone="accent"
              />
              <PreviewRow
                icon={<KeyRound className="size-4" />}
                title="API key issued"
                detail="ci-bot — read+write on acme/*"
                meta="14 minutes ago · admin@acme"
                tone="neutral"
              />
            </ol>
            <p className="mt-4 text-xs text-[var(--color-fg-subtle)]">
              Sketched, not live. Real events flow once FE-API-008 ships.
            </p>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}

function PreviewRow({
  icon,
  title,
  detail,
  meta,
  tone,
}: {
  icon: React.ReactNode;
  title: string;
  detail: string;
  meta: string;
  tone: "success" | "warning" | "accent" | "neutral";
}): React.ReactElement {
  const toneClass = {
    success: "text-[var(--color-success)] bg-[var(--color-success)]/10",
    warning: "text-[var(--color-warning)] bg-[var(--color-warning)]/10",
    accent: "text-[var(--color-accent)] bg-[var(--color-accent-subtle)]",
    neutral: "text-[var(--color-fg-muted)] bg-[var(--color-surface)]",
  }[tone];
  return (
    <li className="flex items-start gap-3">
      <span
        className={`mt-0.5 grid size-7 shrink-0 place-items-center rounded-md ${toneClass}`}
        aria-hidden
      >
        {icon}
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-baseline gap-x-2">
          <span className="text-sm font-medium text-[var(--color-fg)]">
            {title}
          </span>
          <span className="font-mono text-xs text-[var(--color-fg-muted)]">
            {detail}
          </span>
        </div>
        <div className="mt-0.5 text-xs text-[var(--color-fg-subtle)]">{meta}</div>
      </div>
      <Activity className="mt-1 size-3 text-[var(--color-fg-subtle)]" />
    </li>
  );
}

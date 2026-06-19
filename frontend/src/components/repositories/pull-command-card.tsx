import * as React from "react";
import { Terminal } from "lucide-react";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { CopyButton } from "@/components/ui/copy-button";
import { pullCommand } from "@/lib/format";

interface PullCommandCardProps {
  org: string;
  repo: string;
  tag?: string;
}

// Beacon — PullCommandCard. The first thing an operator does on a new
// repository is figure out how to pull from it. We show the command with
// monospace styling and a one-click copy.
//
// FE-API-007 — registry hostname is currently hardcoded; swap to runtime
// when the backend ships the per-tenant host endpoint.
export function PullCommandCard({
  org,
  repo,
  tag = "latest",
}: PullCommandCardProps): React.ReactElement {
  const cmd = pullCommand(org, repo, tag);
  return (
    <Card accentBar="accent">
      <CardHeader className="pb-2">
        <div className="flex items-center justify-between gap-2">
          <div className="flex items-center gap-2 text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            <Terminal className="size-3.5" aria-hidden />
            Pull this image
          </div>
          <CopyButton value={cmd} iconOnly />
        </div>
      </CardHeader>
      <CardContent>
        <pre className="overflow-x-auto rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-4 py-3 font-mono text-sm text-[var(--color-fg)]">
          <span className="mr-2 select-none text-[var(--color-accent)]">$</span>
          {cmd}
        </pre>
      </CardContent>
    </Card>
  );
}

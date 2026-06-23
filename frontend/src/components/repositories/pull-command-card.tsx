import * as React from "react";
import { Terminal, Globe } from "lucide-react";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { CopyButton } from "@/components/ui/copy-button";
import { Badge } from "@/components/ui/badge";
import { pullCommandFor } from "@/lib/format";
import { useWorkspace } from "@/lib/api/workspace";

interface PullCommandCardProps {
  org: string;
  repo: string;
  tag?: string;
  // F4 follow-up — when the repo detail page knows the artifact type
  // (sourced from the `?type=` search param threaded down from /helm or
  // /repositories), the card switches the displayed CLI from
  // `docker pull` to `helm pull oci://...` and the heading from
  // "Pull this image" to "Pull this chart". Undefined falls back to the
  // legacy docker-pull command so older repos + the no-context entry
  // path don't regress.
  artifactType?: string;
}

// Beacon — PullCommandCard. The first thing an operator does on a new
// repository is figure out how to pull from it. We show a numbered
// walkthrough — login (one-time), pull, and an "install / run"
// follow-up — each as its own copyable monospaced line.
//
// FE-API-009 — registry hostname resolves from the live workspace
// metadata, so custom-domain users see their own host immediately.
// `pullCommandFor` does the host-shape detection (local vs. real TLS)
// and tacks on `--plain-http` for helm commands when we're talking to
// the dev gateway. Production charts get clean commands.
//
// Helm specifically (futures.md feedback 2026-06-23): the operator
// usually wants `helm install` after `helm pull`, so we surface both
// as numbered steps rather than just the pull. Reading the card
// top-to-bottom is the exact happy-path.
export function PullCommandCard({
  org,
  repo,
  tag = "latest",
  artifactType,
}: PullCommandCardProps): React.ReactElement {
  const { data: workspace } = useWorkspace();
  const host = workspace?.host;
  const spec = pullCommandFor(artifactType, org, repo, tag, host);
  return (
    <Card accentBar="accent">
      <CardHeader className="pb-2">
        <div className="flex items-center justify-between gap-2">
          <div className="flex items-center gap-2 text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            <Terminal className="size-3.5" aria-hidden />
            {spec.heading}
          </div>
          {workspace?.host ? (
            <Badge
              tone={workspace.host_is_custom ? "accent" : "neutral"}
              className="!py-0 font-mono text-[10px] normal-case tracking-normal"
            >
              <Globe className="size-2.5" aria-hidden />
              {workspace.host_is_custom ? "custom host" : "platform host"}
            </Badge>
          ) : null}
        </div>
      </CardHeader>
      <CardContent className="space-y-2.5">
        {spec.steps.map((step, idx) => (
          <div key={step.label} className="space-y-1">
            <div className="flex items-center justify-between gap-2">
              <span className="text-[11px] font-medium text-[var(--color-fg-muted)]">
                <span className="mr-1.5 inline-block min-w-[1.25em] text-right text-[var(--color-fg-subtle)]">
                  {idx + 1}.
                </span>
                {step.label}
              </span>
              <CopyButton value={step.cmd} iconOnly />
            </div>
            <pre className="overflow-x-auto rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-4 py-2.5 font-mono text-sm text-[var(--color-fg)]">
              <span className="mr-2 select-none text-[var(--color-accent)]">$</span>
              {step.cmd}
            </pre>
          </div>
        ))}
      </CardContent>
    </Card>
  );
}

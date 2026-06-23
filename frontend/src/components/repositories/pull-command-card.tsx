import * as React from "react";
import { Terminal, Globe, Check, Copy } from "lucide-react";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
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
// repository is figure out how to pull from it. We render a faux-
// terminal block — single dark surface with shell-style comment lines
// (`# 1. Login`) above each command and a green `$` prompt — so the
// card reads top-to-bottom like a real shell session the operator can
// paste straight into a terminal. Per-command copy buttons sit on the
// right of each line; hovering them brightens the row so the
// "tap-to-copy" affordance stays discoverable without competing with
// the prompt characters themselves.
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
    <Card accentBar="accent" className="overflow-hidden">
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
      <CardContent className="pt-0">
        {/* Terminal-style block. Fixed dark palette regardless of theme — */}
        {/* shell sessions look uniformly dark in real life, and the */}
        {/* contrast against the surrounding card surface is the visual */}
        {/* cue that says "this is meant to be typed in a terminal." */}
        <div className="rounded-lg border border-slate-700/60 bg-slate-950 shadow-inner">
          {/* macOS-style traffic-light row — purely decorative, but it */}
          {/* sells the "this is a terminal" affordance. Three flat dots */}
          {/* in red/yellow/green ordered to match the canonical layout. */}
          <div className="flex items-center gap-1.5 border-b border-slate-800/80 px-3 py-2">
            <span className="size-2.5 rounded-full bg-red-500/80" aria-hidden />
            <span className="size-2.5 rounded-full bg-yellow-500/80" aria-hidden />
            <span className="size-2.5 rounded-full bg-green-500/80" aria-hidden />
            <span className="ml-2 font-mono text-[10px] text-slate-500">
              {spec.heading.toLowerCase().replace(/\s+/g, "-")} — {host ?? "registry"}
            </span>
          </div>
          {/* Commands. Each step renders a comment line (gray, `# N. …`) */}
          {/* followed by the prompt line (`$ …` with a green prompt). The */}
          {/* copy button is absolutely positioned to the right so the */}
          {/* command text gets the full row width and isn't pushed around */}
          {/* by long commands. Hovering brightens the row + makes the */}
          {/* copy icon legible. */}
          <div className="space-y-3 px-4 py-3 font-mono text-[13px]">
            {spec.steps.map((step, idx) => (
              <TerminalStep
                key={step.label}
                idx={idx + 1}
                label={step.label}
                cmd={step.cmd}
              />
            ))}
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

interface TerminalStepProps {
  idx: number;
  label: string;
  cmd: string;
}

function TerminalStep({ idx, label, cmd }: TerminalStepProps): React.ReactElement {
  const [copied, setCopied] = React.useState(false);

  async function onCopy(): Promise<void> {
    try {
      await navigator.clipboard.writeText(cmd);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard write can fail when the page isn't focused or is
      // served over a context without permission — fall back to a
      // selection so the operator can still Cmd/Ctrl-C manually.
      const sel = window.getSelection();
      sel?.selectAllChildren(document.body);
    }
  }

  return (
    <div className="group relative">
      <div className="text-slate-500 select-none">
        # {idx}. {label}
      </div>
      <div className="flex items-start gap-2 pr-9 text-slate-100">
        <span className="select-none text-emerald-400" aria-hidden>
          $
        </span>
        <span className="break-all">{cmd}</span>
      </div>
      <button
        type="button"
        onClick={() => void onCopy()}
        aria-label={copied ? "Copied" : `Copy: ${label}`}
        className="absolute right-0 top-4 inline-flex items-center justify-center rounded-md border border-slate-700/60 bg-slate-900/70 p-1 text-slate-400 opacity-0 transition hover:border-slate-600 hover:bg-slate-800 hover:text-slate-100 focus:opacity-100 focus:outline-none focus:ring-2 focus:ring-emerald-500/40 group-hover:opacity-100"
      >
        {copied ? (
          <Check className="size-3.5 text-emerald-400" aria-hidden />
        ) : (
          <Copy className="size-3.5" aria-hidden />
        )}
      </button>
    </div>
  );
}

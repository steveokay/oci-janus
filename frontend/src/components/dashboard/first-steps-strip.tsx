import * as React from "react";
import {
  Terminal,
  Globe,
  Check,
  Copy,
  Loader2,
  CheckCircle2,
  ExternalLink,
} from "lucide-react";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { looksLocalHost } from "@/lib/format";
import type { Workspace } from "@/lib/api/workspace";

// Beacon — FirstStepsStrip. DSGN-005 v2.
//
// The original DSGN-005 swapped the dashboard stat row for a 4-card
// vertical FirstSteps stack when the tenant had zero repositories.
// Operators reported losing useful signals at zero repos (workspace
// health, quota allocated, "0/100 GB used"), so this refinement keeps
// the stats always visible and surfaces a single compact horizontal
// "Get started" strip directly below them.
//
// Layout (single Card):
//   1. Header row — eyebrow + endpoint host + plan badge + custom-host
//      badge + "Read the docs" external link.
//   2. Two thin terminal lines — `docker login` and `docker push`, each
//      with a copy button on the right. Mirrors the terminal aesthetic
//      from PullCommandCard but slimmed to single-line snippets so the
//      strip stays one card tall.
//   3. Status footer — polling indicator that flips to a green check
//      when firstRepoSeen=true. The route owns the auto-navigate side
//      effect (DSGN-005 keeps it scoped to the route); this strip is
//      purely visual.
//
// The `tag` step from the old vertical PushCard was dropped — that
// command is informational rather than the canonical happy-path; the
// strip surfaces only login + push so first-run guidance stays one
// glance wide.

interface FirstStepsStripProps {
  workspace: Workspace | null | undefined;
  // True when total_repos has flipped > 0 mid-session. Indicator
  // transitions to a green check; the route handles navigation.
  firstRepoSeen: boolean;
}

const DOCS_URL =
  "https://github.com/steveokay/oci-janus/blob/main/docs/SELF-HOSTING.md";

export function FirstStepsStrip({
  workspace,
  firstRepoSeen,
}: FirstStepsStripProps): React.ReactElement {
  // Same fallback PullCommandCard uses — keeps the copy-pasteable
  // commands sensible when /workspace/me is unwired (TENANT_GRPC_ADDR
  // unset on management).
  const host = workspace?.host ?? "registry.localhost";
  const plan = workspace?.plan;
  const customHost = !!workspace?.host_is_custom;
  const insecureNote = looksLocalHost(host) ? " # dev stack: HTTP" : "";
  const loginCmd = `docker login ${host} -u <user>${insecureNote}`;
  const pushCmd = `docker push ${host}/your-org/image:tag`;

  return (
    <Card accentBar={firstRepoSeen ? "success" : "accent"}>
      <CardHeader className="pb-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          {/* Eyebrow + endpoint identity. Left half. */}
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              Get started
            </span>
            <span className="text-[var(--color-fg-subtle)]">·</span>
            <span className="font-mono text-sm text-[var(--color-fg)]">
              {host}
            </span>
            {plan ? (
              <Badge
                tone={
                  plan === "enterprise"
                    ? "accent"
                    : plan === "pro"
                      ? "success"
                      : "neutral"
                }
                className="!py-0 text-[10px] uppercase tracking-wider"
              >
                {plan}
              </Badge>
            ) : null}
            <Badge
              tone={customHost ? "accent" : "neutral"}
              className="!py-0 font-mono text-[10px] normal-case tracking-normal"
            >
              <Globe className="size-2.5" aria-hidden />
              {customHost ? "custom host" : "platform host"}
            </Badge>
          </div>
          {/* External docs link — right half. */}
          <a
            href={DOCS_URL}
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-1 text-xs font-medium text-[var(--color-accent)] hover:underline"
          >
            <ExternalLink className="size-3" aria-hidden />
            Read the docs
          </a>
        </div>
      </CardHeader>
      <CardContent className="pt-0">
        {/* Two thin terminal lines. Single dark surface — same palette
            as PullCommandCard so the first-run aesthetic and the
            per-repo pull card feel like the same UI family. */}
        <div className="rounded-lg border border-slate-700/60 bg-slate-950 shadow-inner">
          <div className="flex items-center gap-1.5 border-b border-slate-800/80 px-3 py-1.5">
            <span className="size-2 rounded-full bg-red-500/80" aria-hidden />
            <span className="size-2 rounded-full bg-yellow-500/80" aria-hidden />
            <span className="size-2 rounded-full bg-green-500/80" aria-hidden />
            <span className="ml-2 inline-flex items-center gap-1 font-mono text-[10px] text-slate-500">
              <Terminal className="size-3" aria-hidden />
              get-started — {host}
            </span>
          </div>
          <div className="divide-y divide-slate-800/60 font-mono text-[13px]">
            <TerminalLine cmd={loginCmd} ariaLabel="Copy docker login command" />
            <TerminalLine cmd={pushCmd} ariaLabel="Copy docker push command" />
          </div>
        </div>

        {/* Status footer — soft pulse while polling, green check on the
            0→>0 transition. Indicator copy mirrors the old PollingCard
            so the language stays consistent for operators who saw the
            previous shape. */}
        <div className="mt-3 flex items-center gap-2 text-xs">
          {firstRepoSeen ? (
            <>
              <CheckCircle2
                className="size-4 text-[var(--color-success)]"
                aria-hidden
              />
              <span className="font-medium text-[var(--color-fg)]">
                First image received.
              </span>
              <span className="text-[var(--color-fg-muted)]">
                Opening your repositories…
              </span>
            </>
          ) : (
            <>
              <span
                className="relative inline-flex size-4 items-center justify-center"
                aria-hidden
              >
                <span className="absolute inline-flex size-1.5 rounded-full bg-[var(--color-accent)] beacon-pulse" />
                <Loader2 className="size-4 animate-spin text-[var(--color-fg-subtle)]" />
              </span>
              <span className="font-medium text-[var(--color-fg)]">
                Polling for your first image…
              </span>
              <span className="text-[var(--color-fg-muted)]">
                We'll jump to /repositories as soon as the push completes.
              </span>
            </>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

// TerminalLine — single-line copyable shell snippet. The strip uses
// these instead of the numbered TerminalStep from PullCommandCard
// because there's no walkthrough order to communicate at this density
// — each line is a self-contained command the operator runs once.
interface TerminalLineProps {
  cmd: string;
  ariaLabel: string;
}

function TerminalLine({
  cmd,
  ariaLabel,
}: TerminalLineProps): React.ReactElement {
  const [copied, setCopied] = React.useState(false);

  async function onCopy(): Promise<void> {
    try {
      await navigator.clipboard.writeText(cmd);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard write can fail when the page isn't focused — fall
      // back to a selection so the operator can still Cmd/Ctrl-C.
      const sel = window.getSelection();
      sel?.selectAllChildren(document.body);
    }
  }

  return (
    <div className="group relative flex items-center gap-2 px-3 py-2 pr-10 text-slate-100">
      <span className="select-none text-emerald-400" aria-hidden>
        $
      </span>
      <span className="flex-1 truncate">{cmd}</span>
      <button
        type="button"
        onClick={() => void onCopy()}
        aria-label={copied ? "Copied" : ariaLabel}
        className="absolute right-2 top-1/2 inline-flex -translate-y-1/2 items-center justify-center rounded-md border border-slate-700/60 bg-slate-900/70 p-1 text-slate-400 opacity-0 transition hover:border-slate-600 hover:bg-slate-800 hover:text-slate-100 focus:opacity-100 focus:outline-none focus:ring-2 focus:ring-emerald-500/40 group-hover:opacity-100"
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

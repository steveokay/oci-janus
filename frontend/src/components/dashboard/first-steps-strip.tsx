import * as React from "react";
import {
  Terminal,
  Globe,
  Check,
  Copy,
  Loader2,
  CheckCircle2,
  ExternalLink,
  KeyRound,
  Lightbulb,
} from "lucide-react";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { looksLocalHost } from "@/lib/format";
import type { Workspace } from "@/lib/api/workspace";

// Beacon — FirstStepsStrip. DSGN-005 v2.
//
// Single compact card carrying every first-run signal the original
// 4-card FirstSteps stack carried, in a vertical-dense layout that
// keeps the dashboard stats visible above. Holds:
//
//   1. Header — eyebrow + endpoint + platform/custom-host badge +
//      copy-host + "Create API key" + "Read the docs" + tagline.
//      (Plan badge removed per Phase 2.4 / RM-006 — same rationale as
//      the sidebar brand block: no billing on the single-tenant platform.)
//   2. Terminal block — three single-line commands (login / tag /
//      push), each individually copyable. Same dark surface +
//      traffic-light chrome as PullCommandCard so the visual language
//      is shared.
//   3. Hint row — "Replace your-org/your-image with real names."
//   4. Status footer — polling indicator that flips to a green check
//      when firstRepoSeen=true. Route owns the auto-navigate.

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
  const customHost = !!workspace?.host_is_custom;
  const insecureNote = looksLocalHost(host) ? " # dev stack: HTTP" : "";
  const imageRef = `${host}/your-org/your-image:1.0.0`;
  const loginCmd = `docker login ${host} -u <user>${insecureNote}`;
  const tagCmd = `docker tag local-image:latest ${imageRef}`;
  const pushCmd = `docker push ${imageRef}`;

  return (
    <Card accentBar={firstRepoSeen ? "success" : "accent"}>
      <CardHeader className="pb-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          {/* Eyebrow + endpoint identity + copy host. Left half. */}
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              Get started
            </span>
            <span className="text-[var(--color-fg-subtle)]">·</span>
            <span className="font-mono text-sm text-[var(--color-fg)]">
              {host}
            </span>
            <CopyHostButton host={host} />
            <Badge
              tone={customHost ? "accent" : "neutral"}
              className="!py-0 font-mono text-[10px] normal-case tracking-normal"
            >
              <Globe className="size-2.5" aria-hidden />
              {customHost ? "custom host" : "platform host"}
            </Badge>
          </div>
          {/* Action links + tagline — right half. */}
          <div className="flex flex-wrap items-center gap-3">
            <a
              href="/api-keys"
              className="inline-flex items-center gap-1 text-xs font-medium text-[var(--color-accent)] hover:underline"
            >
              <KeyRound className="size-3" aria-hidden />
              Create an API key
            </a>
            <a
              href={DOCS_URL}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 text-xs font-medium text-[var(--color-accent)] hover:underline"
            >
              <ExternalLink className="size-3" aria-hidden />
              Read the docs
            </a>
            <span className="text-[11px] text-[var(--color-fg-subtle)]">
              Three commands to your first push.
            </span>
          </div>
        </div>
      </CardHeader>
      <CardContent className="pt-0">
        {/* Terminal block — three single-line commands stacked tight.
            Single dark surface + traffic-light chrome so the first-run
            and per-repo pull cards feel like the same UI family. */}
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
            <TerminalLine cmd={tagCmd} ariaLabel="Copy docker tag command" />
            <TerminalLine cmd={pushCmd} ariaLabel="Copy docker push command" />
          </div>
        </div>

        {/* Hint + status row — same horizontal density as the header so
            the strip reads as a single compact unit. The hint stays
            visible while polling; once firstRepoSeen flips, the
            indicator takes over the row. */}
        <div className="mt-3 flex flex-wrap items-center justify-between gap-2 text-xs">
          <div className="inline-flex items-center gap-1.5 text-[var(--color-fg-muted)]">
            <Lightbulb className="size-3 text-[var(--color-fg-subtle)]" aria-hidden />
            <span>
              Replace{" "}
              <code className="rounded bg-[var(--color-surface-sunken)] px-1 font-mono text-[11px]">
                your-org/your-image
              </code>{" "}
              with your real org and image name.
            </span>
          </div>
          <div className="inline-flex items-center gap-2">
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
              </>
            )}
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

// CopyHostButton — small affordance next to the endpoint hostname.
// Smaller than the in-terminal copy buttons since the row is dense.
interface CopyHostButtonProps {
  host: string;
}

function CopyHostButton({ host }: CopyHostButtonProps): React.ReactElement {
  const [copied, setCopied] = React.useState(false);
  async function onCopy(): Promise<void> {
    try {
      await navigator.clipboard.writeText(host);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard write fails outside HTTPS / cross-origin frames; the
      // hostname is still selectable text adjacent so the operator can
      // copy via the system shortcut.
    }
  }
  return (
    <button
      type="button"
      onClick={() => void onCopy()}
      aria-label={copied ? "Copied" : "Copy registry host"}
      className="inline-flex size-5 items-center justify-center rounded text-[var(--color-fg-subtle)] hover:bg-[var(--color-surface-sunken)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)] focus-visible:ring-offset-1 focus-visible:ring-offset-[var(--color-surface)]"
    >
      {copied ? (
        <Check className="size-3 text-[var(--color-success)]" aria-hidden />
      ) : (
        <Copy className="size-3" aria-hidden />
      )}
    </button>
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

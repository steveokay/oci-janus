import * as React from "react";
import { Link } from "@tanstack/react-router";
import {
  Terminal,
  Globe,
  Check,
  Copy,
  KeyRound,
  Loader2,
  CheckCircle2,
} from "lucide-react";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { pushCommandFor, looksLocalHost } from "@/lib/format";
import type { Workspace } from "@/lib/api/workspace";

// Beacon — FirstSteps. Rendered on the dashboard when the tenant has
// zero repositories (DSGN-005). The default empty state buried the
// "time-to-first-push" walkthrough behind QuickActions linking to
// already-empty pages; this card stack is the unguided-onboarding fix.
//
// Four cards, in vertical order:
//   1. Registry endpoint — copy host + plan badge.
//   2. `docker login` block (with link to /api-keys to issue credentials).
//   3. `docker tag` + `docker push` block (mirrors PullCommandCard).
//   4. "Waiting for your first image…" polling indicator that flips to
//      a green checkmark when total_repos > 0. The route owns the
//      auto-navigate; this component just renders the visual state.
//
// We intentionally reuse the same terminal-block aesthetic as
// PullCommandCard (rounded slate panel, traffic-light header, monospace
// numbered steps with per-line copy buttons). Consistency between the
// first-run state and the per-repo pull card means an operator's mental
// model carries over to every subsequent push.

interface FirstStepsProps {
  workspace: Workspace | null | undefined;
  // True when total_repos has flipped > 0 mid-session. The indicator
  // card transitions to a green check; the route handles navigation.
  firstRepoSeen: boolean;
}

export function FirstSteps({
  workspace,
  firstRepoSeen,
}: FirstStepsProps): React.ReactElement {
  // host falls back to the dev gateway when /workspace/me is unwired
  // (TENANT_GRPC_ADDR unset on management). Same fallback PullCommandCard
  // uses elsewhere so the copy-pasteable commands stay sensible.
  const host = workspace?.host ?? "registry.localhost";
  const plan = workspace?.plan;
  const customHost = !!workspace?.host_is_custom;

  return (
    <section className="space-y-4">
      <div className="flex items-baseline justify-between gap-2">
        <h2 className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          First steps
        </h2>
        <span className="text-xs text-[var(--color-fg-muted)]">
          Three commands to your first push.
        </span>
      </div>

      <EndpointCard host={host} plan={plan} customHost={customHost} />
      <LoginCard host={host} />
      <PushCard host={host} />
      <PollingCard firstRepoSeen={firstRepoSeen} />
    </section>
  );
}

// ---------------------------------------------------------------------------
// EndpointCard — copy-clickable registry hostname + plan badge.
// ---------------------------------------------------------------------------

interface EndpointCardProps {
  host: string;
  plan: string | undefined;
  customHost: boolean;
}

function EndpointCard({
  host,
  plan,
  customHost,
}: EndpointCardProps): React.ReactElement {
  return (
    <Card accentBar="accent">
      <CardHeader className="pb-2">
        <div className="flex items-center justify-between gap-2">
          <div className="flex items-center gap-2 text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            <Globe className="size-3.5" aria-hidden />
            Your registry endpoint
          </div>
          <div className="flex items-center gap-1.5">
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
        </div>
      </CardHeader>
      <CardContent className="pt-0">
        <InlineCopyRow value={host} ariaLabel={`Copy registry host ${host}`} />
        <p className="mt-2 text-xs text-[var(--color-fg-muted)]">
          Every push and pull from your CI / dev box targets this host.
        </p>
      </CardContent>
    </Card>
  );
}

// InlineCopyRow renders a single monospaced value with a copy button.
// Used by the endpoint card where the value isn't a full shell command —
// just a hostname / identifier the operator wants on their clipboard.
interface InlineCopyRowProps {
  value: string;
  ariaLabel: string;
}

function InlineCopyRow({
  value,
  ariaLabel,
}: InlineCopyRowProps): React.ReactElement {
  const [copied, setCopied] = React.useState(false);

  async function onCopy(): Promise<void> {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // Best-effort — same fallback semantics as CopyButton.
    }
  }

  return (
    <div className="flex items-center gap-2 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3 py-2 font-mono text-sm">
      <span className="flex-1 break-all text-[var(--color-fg)]">{value}</span>
      <button
        type="button"
        onClick={() => void onCopy()}
        aria-label={copied ? "Copied" : ariaLabel}
        className="inline-flex items-center justify-center rounded-md p-1 text-[var(--color-fg-muted)] transition hover:bg-[var(--color-surface)] hover:text-[var(--color-fg)] focus:outline-none focus:ring-2 focus:ring-[var(--color-accent)]"
      >
        {copied ? (
          <Check className="size-3.5 text-[var(--color-success)]" aria-hidden />
        ) : (
          <Copy className="size-3.5" aria-hidden />
        )}
      </button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// LoginCard — `docker login <host>` block + link to issue an API key.
// ---------------------------------------------------------------------------

interface LoginCardProps {
  host: string;
}

function LoginCard({ host }: LoginCardProps): React.ReactElement {
  const insecureNote = looksLocalHost(host) ? " # dev stack: HTTP" : "";
  const cmd = `docker login ${host} -u <user>${insecureNote}`;
  return (
    <Card>
      <CardHeader className="pb-2">
        <div className="flex items-center justify-between gap-2">
          <div className="flex items-center gap-2 text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            <Terminal className="size-3.5" aria-hidden />
            Authenticate your docker CLI
          </div>
          {/* The login walkthrough is incomplete without "where do I get
              a credential" — link directly to the personal API key
              issuance page so the operator can paste a fresh key when
              docker prompts for a password. */}
          <Link
            to="/api-keys"
            className="inline-flex items-center gap-1 text-xs font-medium text-[var(--color-accent)] hover:underline"
          >
            <KeyRound className="size-3" aria-hidden />
            Create an API key
          </Link>
        </div>
      </CardHeader>
      <CardContent className="pt-0">
        <TerminalBlock title={`login — ${host}`}>
          <TerminalStep idx={1} label="Login (one-time)" cmd={cmd} />
        </TerminalBlock>
      </CardContent>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// PushCard — `docker tag` + `docker push` block.
// ---------------------------------------------------------------------------

interface PushCardProps {
  host: string;
}

function PushCard({ host }: PushCardProps): React.ReactElement {
  const spec = pushCommandFor(host);
  return (
    <Card>
      <CardHeader className="pb-2">
        <div className="flex items-center gap-2 text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          <Terminal className="size-3.5" aria-hidden />
          {spec.heading}
        </div>
      </CardHeader>
      <CardContent className="pt-0">
        <TerminalBlock title={`push — ${host}`}>
          {spec.steps.map((step, idx) => (
            <TerminalStep
              key={step.label}
              idx={idx + 1}
              label={step.label}
              cmd={step.cmd}
            />
          ))}
        </TerminalBlock>
        <p className="mt-2 text-xs text-[var(--color-fg-muted)]">
          Replace <span className="font-mono">your-org/your-image</span> with
          your real org and image name. The org will be created on the first
          successful push.
        </p>
      </CardContent>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// PollingCard — "Waiting for your first image…" → green check + navigate.
// The route owns the auto-navigate side effect (DSGN-005 explicitly
// scopes navigate to the route); this card is purely visual.
// ---------------------------------------------------------------------------

interface PollingCardProps {
  firstRepoSeen: boolean;
}

function PollingCard({ firstRepoSeen }: PollingCardProps): React.ReactElement {
  return (
    <Card accentBar={firstRepoSeen ? "success" : "neutral"}>
      <CardContent className="flex items-center gap-3 py-4">
        {firstRepoSeen ? (
          <>
            <CheckCircle2
              className="size-5 text-[var(--color-success)]"
              aria-hidden
            />
            <div className="flex-1">
              <div className="text-sm font-medium text-[var(--color-fg)]">
                First image received.
              </div>
              <div className="text-xs text-[var(--color-fg-muted)]">
                Opening your repositories…
              </div>
            </div>
          </>
        ) : (
          <>
            {/* Soft pulse — communicates "live polling" without spinning
                the operator's attention away from the push commands. */}
            <span
              className="relative inline-flex size-5 items-center justify-center"
              aria-hidden
            >
              <span className="absolute inline-flex size-2 rounded-full bg-[var(--color-accent)] beacon-pulse" />
              <Loader2 className="size-5 animate-spin text-[var(--color-fg-subtle)]" />
            </span>
            <div className="flex-1">
              <div className="text-sm font-medium text-[var(--color-fg)]">
                Waiting for your first image…
              </div>
              <div className="text-xs text-[var(--color-fg-muted)]">
                The dashboard refreshes every 30 seconds; we'll jump to your
                new repo as soon as the push completes.
              </div>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Shared terminal-block primitive — same look as PullCommandCard so the
// first-run guidance and the per-repo pull card feel like the same UI
// family. Lives here (not lifted to ui/) because it's a dashboard-local
// pattern and the two callers don't currently warrant a shared primitive.
// ---------------------------------------------------------------------------

interface TerminalBlockProps {
  title: string;
  children: React.ReactNode;
}

function TerminalBlock({
  title,
  children,
}: TerminalBlockProps): React.ReactElement {
  return (
    <div className="rounded-lg border border-slate-700/60 bg-slate-950 shadow-inner">
      <div className="flex items-center gap-1.5 border-b border-slate-800/80 px-3 py-2">
        <span className="size-2.5 rounded-full bg-red-500/80" aria-hidden />
        <span className="size-2.5 rounded-full bg-yellow-500/80" aria-hidden />
        <span className="size-2.5 rounded-full bg-green-500/80" aria-hidden />
        <span className="ml-2 font-mono text-[10px] text-slate-500">
          {title}
        </span>
      </div>
      <div className="space-y-3 px-4 py-3 font-mono text-[13px]">
        {children}
      </div>
    </div>
  );
}

interface TerminalStepProps {
  idx: number;
  label: string;
  cmd: string;
}

function TerminalStep({
  idx,
  label,
  cmd,
}: TerminalStepProps): React.ReactElement {
  const [copied, setCopied] = React.useState(false);

  async function onCopy(): Promise<void> {
    try {
      await navigator.clipboard.writeText(cmd);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      const sel = window.getSelection();
      sel?.selectAllChildren(document.body);
    }
  }

  return (
    <div className="group relative">
      <div className="select-none text-slate-500">
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

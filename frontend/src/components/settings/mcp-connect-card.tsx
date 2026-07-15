import * as React from "react";
import { Link } from "@tanstack/react-router";
import { toast } from "sonner";
import { Bot, Check, Copy, ExternalLink, KeyRound, ShieldAlert } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useIsGlobalAdmin } from "@/lib/api/abilities";
import { useWorkspace } from "@/lib/api/workspace";
import { useGenerateMcpKey, type GeneratedMcpKey } from "@/lib/api/mcp";
import { buildStdioConfig } from "@/lib/mcp-config";

// MCPConnectCard — Settings › Integrations card that connects an AI agent
// (Claude Desktop / Cursor) to the registry's Model Context Protocol server
// WITHOUT the operator ever hand-assembling a credential (FUT-088 #6 + one-click
// connect).
//
// The pain this removes: the MCP server authenticates with a `key.<id>.<secret>`
// Bearer token, but the UI only ever showed the raw secret once and never the
// composed token or the key UUID — so wiring MCP meant minting a key, digging
// the id out of the app, and concatenating by hand. The "Generate" button now
// mints a dedicated read-only service account + key and bakes the composed token
// straight into a ready-to-paste claude_desktop_config.json.
//
// Admin-gated (renders null for non-admins), matching the sibling PR-registry
// panels as defense in depth on top of the tab gate.

export function MCPConnectCard(): React.ReactElement | null {
  const isAdmin = useIsGlobalAdmin();
  const { data: workspace } = useWorkspace();
  const generate = useGenerateMcpKey();

  // Registry URL the MCP container will call. Defaults to the origin the
  // operator is browsing — for a typical single-host deployment that IS the
  // registry URL. Editable for setups where the MCP client runs elsewhere.
  const [url, setUrl] = React.useState<string>(() =>
    typeof window !== "undefined" ? window.location.origin : "",
  );
  // The freshly-minted key, held only in memory for this session. The secret is
  // unrecoverable once the page is left, hence the shown-once warning.
  const [minted, setMinted] = React.useState<GeneratedMcpKey | null>(null);
  const [copied, setCopied] = React.useState(false);

  // Defense in depth — the tab is already admin-gated, but each panel also
  // renders null for non-admins (matches PRRegistryPanel / PRNamespacesList).
  if (!isAdmin) return null;

  const tenantID = workspace?.tenant_id ?? "";
  const config = buildStdioConfig({
    tenantID,
    managementURL: url,
    apiKey: minted?.token ?? "",
  });

  async function onGenerate(): Promise<void> {
    try {
      // Date.now() only seeds the SA name + guarantees uniqueness; it never
      // reaches a security decision, so a client clock is fine here.
      const result = await generate.mutateAsync(Date.now());
      setMinted(result);
      toast.success("Read-only MCP key generated — copy the config now.");
    } catch {
      toast.error(
        "Couldn't generate the key. You need workspace-admin rights and a reachable auth service.",
      );
    }
  }

  async function onCopy(): Promise<void> {
    try {
      await navigator.clipboard.writeText(config);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard can fail without focus/permission — leave the block
      // selectable so the operator can Cmd/Ctrl-C manually.
    }
  }

  return (
    <Card>
      <CardHeader>
        <div className="flex items-start gap-2">
          <Bot className="mt-0.5 size-4 shrink-0 text-[var(--color-fg-subtle)]" />
          <div className="space-y-1">
            <CardTitle className="text-base">Connect an AI agent (MCP)</CardTitle>
            <CardDescription>
              Expose this registry to Claude Desktop, Cursor, and other MCP
              clients as read-only tools (repositories, tags, scans, signatures,
              audit, service accounts). Generate a scoped key and paste the
              config — no hand-assembled credentials.
            </CardDescription>
          </div>
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        {/* Step 1 — registry URL the MCP container dials. */}
        <label className="block space-y-1">
          <span className="text-sm font-medium text-[var(--color-fg)]">
            Registry URL
          </span>
          <Input
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="https://your-registry.example.com"
            spellCheck={false}
            aria-label="Registry URL"
          />
          <span className="text-xs text-[var(--color-fg-subtle)]">
            The URL the MCP client reaches this registry at. Defaults to this
            site; change it if your agent runs on a different host.
          </span>
        </label>

        {/* Step 2 — one-click mint. */}
        <div className="flex flex-wrap items-center gap-3">
          <Button
            variant="accent"
            onClick={() => void onGenerate()}
            loading={generate.isPending}
            disabled={generate.isPending}
          >
            <KeyRound className="size-4" aria-hidden />
            {minted ? "Generate another key" : "Generate read-only key"}
          </Button>
          <span className="text-xs text-[var(--color-fg-subtle)]">
            Creates a dedicated read-only service account + key and fills it into
            the config below.
          </span>
        </div>

        {/* Shown-once warning — only after a key exists. */}
        {minted ? (
          <div className="flex items-start gap-2 rounded-lg border border-[var(--color-warning-border)] bg-[var(--color-warning-subtle)] p-3 text-xs text-[var(--color-warning)]">
            <ShieldAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
            <div className="space-y-1">
              <p className="font-medium">
                Copy this now — the secret is shown only once.
              </p>
              <p className="text-[var(--color-fg-muted)]">
                It isn't stored and cannot be retrieved later. Created service
                account{" "}
                <code className="font-mono">{minted.saName}</code> — manage or
                revoke it under{" "}
                <Link
                  to="/api-keys/service-accounts"
                  className="text-[var(--color-accent)] hover:underline"
                >
                  API keys › Service accounts
                </Link>
                .
              </p>
            </div>
          </div>
        ) : null}

        {/* The config block — placeholder key before generation, real token after. */}
        <div className="group relative">
          <pre className="overflow-x-auto rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-inset)] p-3 text-xs leading-relaxed">
            <code>{config}</code>
          </pre>
          <button
            type="button"
            onClick={() => void onCopy()}
            aria-label={copied ? "Copied" : "Copy MCP config"}
            className="absolute right-2 top-2 inline-flex items-center justify-center rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] p-1.5 text-[var(--color-fg-subtle)] opacity-0 transition hover:text-[var(--color-fg)] focus:opacity-100 group-hover:opacity-100"
          >
            {copied ? (
              <Check className="size-3.5 text-[var(--color-success)]" aria-hidden />
            ) : (
              <Copy className="size-3.5" aria-hidden />
            )}
          </button>
        </div>

        <p className="text-xs text-[var(--color-fg-muted)]">
          Paste the block into your MCP client's config (Claude Desktop:{" "}
          <code>claude_desktop_config.json</code>) and restart the client. Your
          tenant id is already filled in.
        </p>

        <a
          href="https://steveokay.github.io/oci-janus/integrations/mcp/"
          target="_blank"
          rel="noopener noreferrer"
          className="inline-flex items-center gap-1 text-xs text-[var(--color-accent)] hover:underline"
        >
          Full MCP guide (HTTP transport, Cursor, tool list)
          <ExternalLink className="size-3" aria-hidden />
        </a>
      </CardContent>
    </Card>
  );
}

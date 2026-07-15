import * as React from "react";
import { Link } from "@tanstack/react-router";
import { Bot, Check, Copy, ExternalLink } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { useIsGlobalAdmin } from "@/lib/api/abilities";
import { useWorkspace } from "@/lib/api/workspace";

// MCPConnectCard — Settings › Integrations card that surfaces the registry's
// Model Context Protocol (MCP) server so an operator can connect an AI agent
// (Claude Desktop / Cursor) without leaving the dashboard (FUT-088 paper-cut
// #6). Before this there was zero in-app MCP presence — the setup lived only
// in docs/MCP.md.
//
// The card is informational: it renders the Claude Desktop (stdio) config with
// the live tenant id pre-filled and the API key + registry URL as placeholders
// the operator swaps in. Admin-gated (renders null for non-admins), matching
// the sibling PR-registry panels as defense in depth on top of the tab gate.

// buildStdioConfig renders the claude_desktop_config.json snippet with the
// live tenant id filled in. The API key and management URL stay as
// placeholders — the key is minted per-operator at /api-keys and the external
// URL isn't knowable from the browser.
function buildStdioConfig(tenantID: string): string {
  return JSON.stringify(
    {
      mcpServers: {
        "oci-janus-registry": {
          command: "docker",
          args: [
            "run",
            "-i",
            "--rm",
            "-e",
            "MCP_TRANSPORT=stdio",
            "-e",
            "MCP_MANAGEMENT_URL=https://your-registry.example.com",
            "-e",
            "MCP_API_KEY=key.<uuid>.<secret>",
            "-e",
            `MCP_TENANT_ID=${tenantID || "<tenant-id>"}`,
            "steveokay/oci-janus-mcp:latest",
          ],
        },
      },
    },
    null,
    2,
  );
}

export function MCPConnectCard(): React.ReactElement | null {
  const isAdmin = useIsGlobalAdmin();
  const { data: workspace } = useWorkspace();
  const [copied, setCopied] = React.useState(false);

  // Defense in depth — the tab is already admin-gated, but each panel also
  // renders null for non-admins (matches PRRegistryPanel / PRNamespacesList).
  if (!isAdmin) return null;

  const config = buildStdioConfig(workspace?.tenant_id ?? "");

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
              clients as read-only tools (repositories, tags, scans,
              signatures, audit, service accounts). The agent queries live data
              through the management API.
            </CardDescription>
          </div>
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        <ol className="space-y-2 text-sm text-[var(--color-fg-muted)]">
          <li>
            <span className="font-medium text-[var(--color-fg)]">
              1. Mint a read-only key.
            </span>{" "}
            Create a service account under{" "}
            <Link
              to="/api-keys/service-accounts"
              className="text-[var(--color-accent)] hover:underline"
            >
              API keys › Service accounts
            </Link>{" "}
            and issue a key scoped to <code>repo:read</code>,{" "}
            <code>scan:read</code>, <code>audit:read</code>,{" "}
            <code>access:read</code> (and <code>signer:read</code> for
            signature queries). Copy the <code>key.&lt;uuid&gt;.&lt;secret&gt;</code>{" "}
            — it's shown once.
          </li>
          <li>
            <span className="font-medium text-[var(--color-fg)]">
              2. Paste the config.
            </span>{" "}
            Drop the block below into your MCP client's config (Claude Desktop:{" "}
            <code>claude_desktop_config.json</code>), swap in your registry URL
            and the key from step 1, then restart the client. Your tenant id is
            already filled in.
          </li>
        </ol>

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

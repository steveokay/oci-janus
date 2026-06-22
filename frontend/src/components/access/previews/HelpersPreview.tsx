import * as React from "react";
import { Check, Copy } from "lucide-react";
import { PreviewBanner } from "@/components/access/PreviewBanner";

// Code snippets keyed by tab — illustrative dummy values only (FUT-002 preview).
// The "ci-prod" key name and the placeholder credentials are not real.
const SNIPPETS = {
  "docker login": `# Authenticate Docker to the registry using your API key.
# Replace <REGISTRY_HOST> with your tenant's registry hostname.
echo "$REGISTRY_API_KEY" | docker login <REGISTRY_HOST> \\
  --username ci-prod \\
  --password-stdin`,

  "kubernetes Secret": `# Kubernetes pull secret — base64-encoded dockerconfigjson.
# Generate the .dockerconfigjson value with:
#   kubectl create secret docker-registry regcred \\
#     --docker-server=<REGISTRY_HOST> \\
#     --docker-username=ci-prod \\
#     --docker-password=$REGISTRY_API_KEY \\
#     --dry-run=client -o yaml
apiVersion: v1
kind: Secret
metadata:
  name: regcred
type: kubernetes.io/dockerconfigjson
data:
  .dockerconfigjson: <BASE64_ENCODED_DOCKER_CONFIG>`,

  terraform: `# Terraform Docker provider — authenticates with the registry.
# Store REGISTRY_API_KEY in a tfvars file or a secrets manager.
provider "docker" {
  host = "tcp://localhost:2375/"

  registry_auth {
    address  = var.registry_host
    username = "ci-prod"
    password = var.registry_api_key
  }
}

variable "registry_host"    { type = string }
variable "registry_api_key" {
  type      = string
  sensitive = true
}`,

  "GitHub Actions": `# GitHub Actions — authenticate then push an image.
# Store REGISTRY_API_KEY as a repository or organisation secret.
- name: Log in to registry
  uses: docker/login-action@v3
  with:
    registry: \${{ vars.REGISTRY_HOST }}
    username: ci-prod
    password: \${{ secrets.REGISTRY_API_KEY }}

- name: Build and push
  uses: docker/build-push-action@v6
  with:
    push: true
    tags: \${{ vars.REGISTRY_HOST }}/myorg/myimage:latest`,
} as const;

type TabKey = keyof typeof SNIPPETS;
const TABS: TabKey[] = [
  "docker login",
  "kubernetes Secret",
  "terraform",
  "GitHub Actions",
];

// HelpersPreview — illustrative preview of the credential-helpers surface
// (FUT-002, shipping Sprint 11). The key selector is disabled; code tabs are
// read-only. Copy buttons are FUNCTIONAL — they copy the static snippet to the
// clipboard so operators can still try the workflow.
export function HelpersPreview(): React.ReactElement {
  const [activeTab, setActiveTab] = React.useState<TabKey>("docker login");
  // Tracks which tab's copy button recently fired, to show a brief ✓ tick.
  const [copiedTab, setCopiedTab] = React.useState<TabKey | null>(null);

  // handleCopy — writes the current tab's snippet to the clipboard.
  // Falls back silently if the Clipboard API is unavailable (e.g. non-secure
  // context in some CI preview deployments).
  async function handleCopy(tab: TabKey): Promise<void> {
    try {
      await navigator.clipboard.writeText(SNIPPETS[tab]);
      setCopiedTab(tab);
      // Reset the tick after 2 s so the button returns to its default state.
      setTimeout(() => setCopiedTab(null), 2000);
    } catch {
      // Clipboard API unavailable — fail silently; the snippet is still visible.
    }
  }

  return (
    <div className="space-y-6">
      {/* Page header. */}
      <header className="flex flex-col gap-1">
        <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          Preview
        </p>
        <h1 className="font-display text-3xl font-medium tracking-tight">
          Credential helpers
        </h1>
        <p className="text-sm text-[var(--color-fg-muted)]">
          Copy ready-made snippets for docker, Kubernetes, Terraform, or GitHub
          Actions — pre-filled with your chosen API key.
        </p>
      </header>

      {/* Amber preview notice. */}
      <PreviewBanner sprint="Sprint 11" futureID="FUT-002" />

      {/* Hidden reason text for AT. */}
      <p id="helpers-disabled-reason" className="sr-only">
        Available in Sprint 11 (FUT-002). The key selector is not yet
        functional. Snippets shown are illustrative; copy still works.
      </p>

      {/* Key selector — disabled (real key population ships in Sprint 11). */}
      <div>
        <label
          htmlFor="helper-key-select"
          className="mb-1 block text-sm font-medium text-[var(--color-fg-muted)]"
        >
          API key
        </label>
        <select
          id="helper-key-select"
          disabled
          aria-disabled="true"
          aria-describedby="helpers-disabled-reason"
          defaultValue="ci-prod"
          className="w-64 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-surface)] px-3 py-2 text-sm opacity-60 cursor-not-allowed"
        >
          <option value="ci-prod">ci-prod (illustrative)</option>
        </select>
      </div>

      {/* Code snippet area. */}
      <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-surface)]">
        {/* Tab bar. */}
        <div
          role="tablist"
          aria-label="Snippet format"
          className="flex gap-0 border-b border-[var(--color-border)]"
        >
          {TABS.map((tab) => (
            <button
              key={tab}
              role="tab"
              id={`helper-tab-${tab.replace(/\s+/g, "-")}`}
              aria-selected={activeTab === tab}
              aria-controls={`helper-panel-${tab.replace(/\s+/g, "-")}`}
              type="button"
              onClick={() => setActiveTab(tab)}
              className={[
                "px-4 py-2.5 text-xs font-medium transition-colors first:rounded-tl-lg",
                activeTab === tab
                  ? "border-b-2 border-[var(--color-accent)] text-[var(--color-accent)]"
                  : "text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]",
              ].join(" ")}
            >
              {tab}
            </button>
          ))}
        </div>

        {/* Code panel + copy button. */}
        {TABS.map((tab) => (
          <div
            key={tab}
            role="tabpanel"
            id={`helper-panel-${tab.replace(/\s+/g, "-")}`}
            aria-labelledby={`helper-tab-${tab.replace(/\s+/g, "-")}`}
            hidden={activeTab !== tab}
            className="relative"
          >
            <pre className="overflow-x-auto rounded-b-lg font-mono text-sm bg-[var(--color-bg-code,_var(--color-bg-subtle))] p-3 text-[var(--color-fg)]">
              <code>{SNIPPETS[tab]}</code>
            </pre>

            {/* Copy button — FUNCTIONAL: copies the static snippet text. */}
            <button
              type="button"
              onClick={() => void handleCopy(tab)}
              aria-label={
                copiedTab === tab
                  ? "Copied to clipboard"
                  : `Copy ${tab} snippet to clipboard`
              }
              className="absolute right-3 top-3 flex items-center gap-1.5 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-surface)] px-2 py-1 text-xs text-[var(--color-fg-muted)] transition-colors hover:text-[var(--color-fg)]"
            >
              {copiedTab === tab ? (
                <Check className="size-3 text-green-500" aria-hidden />
              ) : (
                <Copy className="size-3" aria-hidden />
              )}
              {copiedTab === tab ? "Copied" : "Copy"}
            </button>
          </div>
        ))}
      </div>
    </div>
  );
}

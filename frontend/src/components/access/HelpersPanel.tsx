import * as React from "react";
import { Check, Copy } from "lucide-react";
import { useRegistryInfo } from "@/lib/api/registry-info";
import { useServiceAccounts } from "@/lib/api/service-accounts";
import {
  buildSnippets,
  SNIPPET_FORMATS,
  type SnippetFormat,
} from "@/lib/credential-snippets";

// HelpersPanel — live FUT-002 credential-helpers surface. Replaces the
// preview component. Renders copy-paste-ready docker / k8s / terraform /
// GHA snippets parameterised on the operator's selected service account +
// the deployment's registry hostname.
//
// Secrets are NEVER rendered into the snippets — every format references
// $REGISTRY_API_KEY (or secrets.REGISTRY_API_KEY in GHA). The operator
// supplies the secret out of band at runtime.
export function HelpersPanel(): React.ReactElement {
  const registryInfo = useRegistryInfo();
  const serviceAccounts = useServiceAccounts();

  const [activeTab, setActiveTab] = React.useState<SnippetFormat>(
    "docker login",
  );
  const [activeSAId, setActiveSAId] = React.useState<string>("");
  const [copiedTab, setCopiedTab] = React.useState<SnippetFormat | null>(null);

  // Default the picker to the first active SA once data lands.
  React.useEffect(() => {
    if (
      !activeSAId &&
      serviceAccounts.data &&
      serviceAccounts.data.length > 0
    ) {
      setActiveSAId(serviceAccounts.data[0].id);
    }
  }, [activeSAId, serviceAccounts.data]);

  const isLoading = registryInfo.isLoading || serviceAccounts.isLoading;
  const hasError = registryInfo.isError || serviceAccounts.isError;

  const activeSA = serviceAccounts.data?.find((sa) => sa.id === activeSAId);
  const snippets =
    activeSA && registryInfo.data
      ? buildSnippets({
          hostname: registryInfo.data.registry_host,
          saName: activeSA.name,
        })
      : null;

  // handleCopy — writes the active tab's snippet to the clipboard and
  // shows a brief tick. Fails silently when the Clipboard API is
  // unavailable (e.g. non-secure context).
  async function handleCopy(tab: SnippetFormat): Promise<void> {
    if (!snippets) return;
    try {
      await navigator.clipboard.writeText(snippets[tab]);
      setCopiedTab(tab);
      setTimeout(() => setCopiedTab(null), 2000);
    } catch {
      /* Clipboard API unavailable — fail silently. */
    }
  }

  return (
    <div className="space-y-6">
      <header className="flex flex-col gap-1">
        <h1 className="font-display text-3xl font-medium tracking-tight">
          Credential helpers
        </h1>
        <p className="text-sm text-[var(--color-fg-muted)]">
          Copy-paste-ready authentication snippets parameterised on your
          selected service account and registry hostname.
        </p>
      </header>

      {isLoading ? (
        <div role="status" className="text-sm text-[var(--color-fg-muted)]">
          Loading credential helpers&hellip;
        </div>
      ) : hasError || !registryInfo.data ? (
        <div role="alert" className="text-sm text-red-600">
          Failed to load credential helpers. Try refreshing the page.
        </div>
      ) : (
        <>
      {/* Service-account picker. */}
      <label className="block text-sm">
        <span className="mb-1 block text-xs font-medium text-[var(--color-fg-muted)]">
          Service account
        </span>
        <select
          value={activeSAId}
          onChange={(e) => setActiveSAId(e.target.value)}
          className="w-full max-w-sm rounded-md border border-[var(--color-border)] bg-[var(--color-bg-surface)] px-3 py-2 text-sm"
        >
          {serviceAccounts.data?.map((sa) => (
            <option key={sa.id} value={sa.id}>
              {sa.name}
              {sa.disabled_at ? " (disabled)" : ""}
            </option>
          ))}
        </select>
      </label>

      {/* Format tabs. */}
      <div
        role="tablist"
        aria-label="Snippet format"
        className="flex flex-wrap gap-2 border-b border-[var(--color-border)]"
      >
        {SNIPPET_FORMATS.map((tab) => (
          <button
            key={tab}
            role="tab"
            aria-selected={activeTab === tab}
            type="button"
            onClick={() => setActiveTab(tab)}
            className={[
              "px-3 py-2 text-sm font-medium",
              activeTab === tab
                ? "border-b-2 border-[var(--color-accent)] text-[var(--color-fg)]"
                : "text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]",
            ].join(" ")}
          >
            {tab}
          </button>
        ))}
      </div>

      {/* Snippet body + copy button. */}
      {snippets ? (
        <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-subtle)]">
          <div className="flex items-center justify-between border-b border-[var(--color-border)] px-4 py-2">
            <span className="font-mono text-xs text-[var(--color-fg-muted)]">
              {activeTab}
            </span>
            <button
              type="button"
              onClick={() => handleCopy(activeTab)}
              className="inline-flex items-center gap-1.5 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-surface)] px-2.5 py-1 text-xs"
              aria-label={`Copy ${activeTab} snippet`}
            >
              {copiedTab === activeTab ? (
                <>
                  <Check className="size-3.5" aria-hidden /> Copied
                </>
              ) : (
                <>
                  <Copy className="size-3.5" aria-hidden /> Copy
                </>
              )}
            </button>
          </div>
          <pre className="overflow-x-auto p-4 font-mono text-xs leading-relaxed">
            <code>{snippets[activeTab]}</code>
          </pre>
        </div>
      ) : (
        <p className="text-sm text-[var(--color-fg-muted)]">
          Create a service account first to see helpers.
        </p>
      )}
        </>
      )}
    </div>
  );
}

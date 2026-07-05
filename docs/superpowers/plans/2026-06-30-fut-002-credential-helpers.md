# FUT-002 Credential Helpers Implementation Plan

> **✅ SHIPPED — PR #221. Plan complete; canonical status in `status.md` / `FE-STATUS.md`. Task checkboxes left unticked — this is a subagent-driven execution artifact, not a live tracker.**

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Lift `/api-keys/helpers` from preview to live — render hostname-aware `docker login` / k8s Secret / Terraform / GHA snippets parameterised on the operator's selected service account.

**Architecture:** New BFF endpoint `GET /api/v1/registry-info` returns the deployment's registry hostname (driven by a new `PLATFORM_HOST` env on `services/management`). The FE swaps the preview component for a live `HelpersPanel` that composes snippets from `useRegistryInfo()` + the existing `useServiceAccounts()` hook + a pure `buildSnippets()` helper.

**Tech Stack:** Go 1.25 / `net/http` (BFF), React 18 / TanStack Query / Vitest (FE), zero new dependencies, zero schema changes, no proto change.

**Spec:** [`../specs/2026-06-30-api-keys-tier2-backend-design.md`](../specs/2026-06-30-api-keys-tier2-backend-design.md) §Feature 1.

**Branch:** `feat/fut-002-credential-helpers` (already created off `main`; spec doc already committed as `7dcfda7`).

---

## File Structure

**Created:**
- `frontend/src/lib/api/registry-info.ts` — `useRegistryInfo()` TanStack hook + `RegistryInfo` type
- `frontend/src/lib/credential-snippets.ts` — pure `buildSnippets({hostname, saName})` helper
- `frontend/src/lib/__tests__/credential-snippets.test.ts` — unit tests for the helper
- `frontend/src/components/access/HelpersPanel.tsx` — live panel (replaces preview)
- `frontend/src/components/access/__tests__/HelpersPanel.test.tsx` — component tests
- `services/management/internal/handler/registry_info.go` — BFF handler
- `services/management/internal/handler/registry_info_test.go` — handler tests

**Modified:**
- `services/management/internal/config/config.go` — add `PlatformHost string` + production validation
- `services/management/internal/handler/handler.go` — add `platformHost` field + `WithPlatformHost(host)` setter + route registration
- `services/management/cmd/server/main.go` — pass `cfg.PlatformHost` to `WithPlatformHost`
- `infra/docker-compose/docker-compose.yml` — add `PLATFORM_HOST: localhost:8080` on `registry-management`
- `frontend/src/routes/_authenticated.api-keys.helpers.tsx` — swap `HelpersPreview` for `HelpersPanel`

**Deleted:**
- `frontend/src/components/access/previews/HelpersPreview.tsx` — replaced by `HelpersPanel`

**Tracker hygiene:**
- `status-tracker.md` — add `REM-021` entry when work starts; remove on merge
- `status.md` — append resolution row on merge
- `futures.md` — replace FUT-002 body with `**DONE — see status.md (REM-021)**` stub

---

## Task 1: Add `PLATFORM_HOST` config to `services/management`

**Files:**
- Modify: `services/management/internal/config/config.go`

- [ ] **Step 1.1: Add the `PlatformHost` field to `Config`**

Edit `services/management/internal/config/config.go`, add after the `WebhookGRPCAddr` block (line 30):

```go
	// PlatformHost is the externally-reachable hostname of the registry
	// (e.g. "registry.example.com" or "localhost:8080" in dev). Used by the
	// credential-helpers (/api-keys/helpers) surface to render copy-paste
	// snippets — operators copy `docker login <PlatformHost>` etc., so this
	// must match what their CI runner will see. Required in production.
	PlatformHost string `mapstructure:"PLATFORM_HOST"`
```

- [ ] **Step 1.2: Add production validation**

In the same file, edit `validate(cfg)` (line 111), add inside the production block (after the CORS check):

```go
		if cfg.PlatformHost == "" {
			return fmt.Errorf("PLATFORM_HOST is required in production")
		}
```

- [ ] **Step 1.3: Commit**

```bash
git add services/management/internal/config/config.go
git commit -m "feat(management): add PLATFORM_HOST config for credential helpers (FUT-002)"
```

---

## Task 2: Plumb `platformHost` through `Handler`

**Files:**
- Modify: `services/management/internal/handler/handler.go`

- [ ] **Step 2.1: Add the field to the `Handler` struct**

After the `buildVersion string` field (around line 107) add:

```go
	// platformHost is the externally-reachable registry hostname (e.g.
	// "registry.example.com"). Injected via WithPlatformHost; used by
	// handleRegistryInfo for the FUT-002 credential-helpers surface.
	// Empty in dev when the env var isn't set; the helper endpoint returns
	// 500 in production with an empty value (the config layer rejects this
	// at startup anyway).
	platformHost string
```

- [ ] **Step 2.2: Add the `WithPlatformHost` setter**

After the existing `WithDeploymentInfo` method (around line 227) add:

```go
// WithPlatformHost wires the externally-reachable registry hostname that the
// FUT-002 credential-helpers surface returns from GET /api/v1/registry-info.
// Returns the handler for chained initialization, mirroring WithDeploymentInfo.
func (h *Handler) WithPlatformHost(host string) *Handler {
	h.platformHost = host
	return h
}
```

- [ ] **Step 2.3: Commit**

```bash
git add services/management/internal/handler/handler.go
git commit -m "feat(management): plumb platformHost through Handler (FUT-002)"
```

---

## Task 3: Write failing test for `GET /api/v1/registry-info`

**Files:**
- Create: `services/management/internal/handler/registry_info_test.go`

- [ ] **Step 3.1: Write the test file**

```go
package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRegistryInfo covers the FUT-002 GET /api/v1/registry-info endpoint.
// The endpoint is unauthenticated by design (parallels handleDeploymentInfo)
// and leaks no tenant data — just the deployment's registry hostname so the
// FE credential-helpers surface can render copy-paste snippets.
func TestRegistryInfo_ReturnsConfiguredHost(t *testing.T) {
	h := New(nil, nil, nil, nil, "")
	h = h.WithPlatformHost("registry.example.com")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/registry-info", nil)
	rec := httptest.NewRecorder()

	h.handleRegistryInfo(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body struct {
		RegistryHost     string `json:"registry_host"`
		SupportsOCIV11   bool   `json:"supports_oci_v1_1"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.RegistryHost != "registry.example.com" {
		t.Errorf("registry_host = %q, want %q", body.RegistryHost, "registry.example.com")
	}
	if !body.SupportsOCIV11 {
		t.Errorf("supports_oci_v1_1 = false, want true")
	}
}

// TestRegistryInfo_EmptyHostReturns500 covers the dev-misconfig case where
// PLATFORM_HOST wasn't set. We fail loud rather than returning an empty
// string the FE would then render as "docker login   " (two spaces).
func TestRegistryInfo_EmptyHostReturns500(t *testing.T) {
	h := New(nil, nil, nil, nil, "")
	// Note: WithPlatformHost NOT called.

	req := httptest.NewRequest(http.MethodGet, "/api/v1/registry-info", nil)
	rec := httptest.NewRecorder()

	h.handleRegistryInfo(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when PLATFORM_HOST unset", rec.Code)
	}
}
```

- [ ] **Step 3.2: Run the test, confirm it fails on `h.handleRegistryInfo undefined`**

```bash
cd services/management && go test ./internal/handler/ -run TestRegistryInfo -v
```

Expected: compile error `h.handleRegistryInfo undefined`.

- [ ] **Step 3.3: Commit the failing test**

```bash
git add services/management/internal/handler/registry_info_test.go
git commit -m "test(management): registry-info handler contract (FUT-002)"
```

---

## Task 4: Implement `handleRegistryInfo`

**Files:**
- Create: `services/management/internal/handler/registry_info.go`

- [ ] **Step 4.1: Write the handler**

```go
package handler

import (
	"encoding/json"
	"net/http"
)

// handleRegistryInfo returns the deployment's externally-reachable registry
// hostname so the FE credential-helpers surface (/api-keys/helpers) can
// render copy-paste-ready `docker login`, k8s Secret, Terraform, and GHA
// snippets without operators having to type the hostname.
//
// Unauthenticated by design (mirrors handleDeploymentInfo) — leaks no
// tenant data, only deployment metadata. Cached aggressively by the FE.
//
// Returns 500 with a clear error body when PLATFORM_HOST is empty, rather
// than returning an empty string the FE would render as "docker login  "
// (two spaces). The config layer's production validator catches this at
// startup; this guard handles the dev-misconfig case.
//
// FUT-002 — see docs/superpowers/specs/2026-06-30-api-keys-tier2-backend-design.md.
func (h *Handler) handleRegistryInfo(w http.ResponseWriter, r *http.Request) {
	if h.platformHost == "" {
		http.Error(w, `{"error":"PLATFORM_HOST not configured"}`, http.StatusInternalServerError)
		return
	}
	body := map[string]any{
		"registry_host":      h.platformHost,
		"supports_oci_v1_1":  true,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}
```

- [ ] **Step 4.2: Run the tests, confirm both pass**

```bash
cd services/management && go test ./internal/handler/ -run TestRegistryInfo -v
```

Expected: `PASS` on both `TestRegistryInfo_ReturnsConfiguredHost` and `TestRegistryInfo_EmptyHostReturns500`.

- [ ] **Step 4.3: Commit**

```bash
git add services/management/internal/handler/registry_info.go
git commit -m "feat(management): GET /api/v1/registry-info for credential helpers (FUT-002)"
```

---

## Task 5: Register the route

**Files:**
- Modify: `services/management/internal/handler/handler.go`

- [ ] **Step 5.1: Add the route mux entry**

In `services/management/internal/handler/handler.go`, near the existing `mux.Handle("GET /api/v1/deployment-info", ...)` line (around 279), add immediately after it:

```go
	mux.Handle("GET /api/v1/registry-info", http.HandlerFunc(h.handleRegistryInfo))
```

- [ ] **Step 5.2: Verify the build still compiles + all handler tests still pass**

```bash
cd services/management && go build ./... && go test ./internal/handler/ -v
```

Expected: build OK; all tests pass (including pre-existing).

- [ ] **Step 5.3: Commit**

```bash
git add services/management/internal/handler/handler.go
git commit -m "feat(management): register GET /api/v1/registry-info route (FUT-002)"
```

---

## Task 6: Wire `main.go` + docker-compose env

**Files:**
- Modify: `services/management/cmd/server/main.go`
- Modify: `infra/docker-compose/docker-compose.yml`

- [ ] **Step 6.1: Pass `PlatformHost` to the Handler in `main.go`**

Find the existing `.WithDeploymentInfo(...)` chain call in `services/management/cmd/server/main.go` and add `.WithPlatformHost(cfg.PlatformHost)` immediately after it. Example:

```go
	h := handler.New(authClient, metaClient, auditClient, pub, cfg.PlatformAdminTenantID, healthClients...).
		WithPublisher(pub).
		WithDeploymentInfo(cfg.DeploymentMode, cfg.BuildVersion).
		WithPlatformHost(cfg.PlatformHost)
```

(Adjust the chain to the existing exact shape — copy the line above `WithDeploymentInfo`, swap the method name.)

- [ ] **Step 6.2: Add the env var to docker-compose**

In `infra/docker-compose/docker-compose.yml`, find the `registry-management` service block, locate its `environment:` map, and add:

```yaml
      PLATFORM_HOST: localhost:8080
```

Place it alphabetically with the other `PLATFORM_*` keys.

- [ ] **Step 6.3: Verify the compose stack still parses**

```bash
docker compose -f infra/docker-compose/docker-compose.yml config > /dev/null && echo "compose OK"
```

Expected: `compose OK` (no parse errors).

- [ ] **Step 6.4: Commit**

```bash
git add services/management/cmd/server/main.go infra/docker-compose/docker-compose.yml
git commit -m "feat(management): wire PLATFORM_HOST through main + compose (FUT-002)"
```

---

## Task 7: FE — `useRegistryInfo` TanStack hook

**Files:**
- Create: `frontend/src/lib/api/registry-info.ts`

- [ ] **Step 7.1: Write the hook**

```typescript
import { useQuery } from "@tanstack/react-query";
import { apiClient } from "./client";

// RegistryInfo mirrors the JSON returned by GET /api/v1/registry-info
// (services/management/internal/handler/registry_info.go).
export interface RegistryInfo {
  registry_host: string;
  supports_oci_v1_1: boolean;
}

// useRegistryInfo fetches the deployment's externally-reachable registry
// hostname for use in the credential-helpers (/api-keys/helpers) surface.
// Aggressively cached (10-minute staleTime) — the hostname doesn't change
// during a session.
export function useRegistryInfo() {
  return useQuery<RegistryInfo>({
    queryKey: ["registry-info"],
    queryFn: () => apiClient.get<RegistryInfo>("/api/v1/registry-info"),
    staleTime: 10 * 60 * 1000,
    gcTime: 30 * 60 * 1000,
  });
}
```

- [ ] **Step 7.2: Commit**

```bash
git add frontend/src/lib/api/registry-info.ts
git commit -m "feat(frontend): useRegistryInfo TanStack hook (FUT-002)"
```

---

## Task 8: FE — `buildSnippets` pure helper + tests

**Files:**
- Create: `frontend/src/lib/credential-snippets.ts`
- Create: `frontend/src/lib/__tests__/credential-snippets.test.ts`

- [ ] **Step 8.1: Write the test first**

```typescript
import { describe, it, expect } from "vitest";
import { buildSnippets, SNIPPET_FORMATS } from "../credential-snippets";

describe("buildSnippets", () => {
  const hostname = "registry.example.com";
  const saName = "ci-prod";

  it("renders all four supported formats", () => {
    const snippets = buildSnippets({ hostname, saName });
    expect(Object.keys(snippets).sort()).toEqual([...SNIPPET_FORMATS].sort());
  });

  it("substitutes the hostname into the docker login snippet", () => {
    const { "docker login": s } = buildSnippets({ hostname, saName });
    expect(s).toContain("registry.example.com");
    expect(s).toContain("--username ci-prod");
    // No <REGISTRY_HOST> placeholder should leak through.
    expect(s).not.toContain("<REGISTRY_HOST>");
  });

  it("substitutes the hostname into the kubernetes Secret snippet", () => {
    const { "kubernetes Secret": s } = buildSnippets({ hostname, saName });
    expect(s).toContain("--docker-server=registry.example.com");
    expect(s).toContain("--docker-username=ci-prod");
    expect(s).not.toContain("<REGISTRY_HOST>");
  });

  it("substitutes the hostname into the terraform snippet", () => {
    const { terraform: s } = buildSnippets({ hostname, saName });
    expect(s).toContain('username = "ci-prod"');
    expect(s).toContain("registry.example.com");
  });

  it("substitutes the hostname into the GitHub Actions snippet", () => {
    const { "GitHub Actions": s } = buildSnippets({ hostname, saName });
    expect(s).toContain("registry: registry.example.com");
    expect(s).toContain("username: ci-prod");
  });

  it("escapes special characters in the service-account name", () => {
    const { "docker login": s } = buildSnippets({
      hostname,
      saName: 'evil"name',
    });
    // The renderer must not break shell-quoting — embedded double-quote
    // gets escaped or the SA name gets rejected at create-time. Today the
    // SA name regex disallows ", so this is defence in depth.
    expect(s).not.toContain('--username evil"name');
  });
});
```

- [ ] **Step 8.2: Run the test, confirm it fails on missing module**

```bash
cd frontend && npm run test -- --run credential-snippets
```

Expected: `Failed to load module '../credential-snippets'`.

- [ ] **Step 8.3: Implement the helper**

```typescript
// credential-snippets — pure, side-effect-free renderers for the FUT-002
// helpers surface. Returned strings are copy-paste-ready; callers add
// nothing else.
//
// Hostname is the externally-reachable registry hostname returned by
// GET /api/v1/registry-info. saName is the human-readable service-account
// name (NOT the secret) — only used for the --username field so the snippet
// is recognisable in CI logs.
//
// The secret itself is NEVER baked into the snippet; every snippet
// references an env var ($REGISTRY_API_KEY) the operator has to provide
// out of band. That's both a security posture (the dashboard doesn't echo
// secret material) and a usability posture (the snippet is shareable).

export const SNIPPET_FORMATS = [
  "docker login",
  "kubernetes Secret",
  "terraform",
  "GitHub Actions",
] as const;

export type SnippetFormat = (typeof SNIPPET_FORMATS)[number];

export interface SnippetInputs {
  hostname: string;
  saName: string;
}

// Reject names that would break shell quoting — defence in depth on top
// of the SA-name regex enforced at create time.
function sanitiseSAName(name: string): string {
  // Drop double-quote, backtick, dollar, and backslash. The remaining set
  // (lowercase + digits + `_-`) is safe in every shell + YAML context.
  return name.replace(/["`$\\]/g, "");
}

export function buildSnippets({
  hostname,
  saName,
}: SnippetInputs): Record<SnippetFormat, string> {
  const safe = sanitiseSAName(saName);
  return {
    "docker login": [
      `# Authenticate Docker to the registry using your API key.`,
      `# Replace $REGISTRY_API_KEY with the secret you copied at key creation.`,
      `echo "$REGISTRY_API_KEY" | docker login ${hostname} \\`,
      `  --username ${safe} \\`,
      `  --password-stdin`,
    ].join("\n"),

    "kubernetes Secret": [
      `# Kubernetes pull secret — generated via kubectl.`,
      `kubectl create secret docker-registry regcred \\`,
      `  --docker-server=${hostname} \\`,
      `  --docker-username=${safe} \\`,
      `  --docker-password=$REGISTRY_API_KEY \\`,
      `  --dry-run=client -o yaml`,
    ].join("\n"),

    terraform: [
      `# Terraform Docker provider — authenticates with the registry.`,
      `provider "docker" {`,
      `  registry_auth {`,
      `    address  = "${hostname}"`,
      `    username = "${safe}"`,
      `    password = var.registry_api_key`,
      `  }`,
      `}`,
      ``,
      `variable "registry_api_key" {`,
      `  type      = string`,
      `  sensitive = true`,
      `}`,
    ].join("\n"),

    "GitHub Actions": [
      `# GitHub Actions — authenticate then push.`,
      `- name: Log in to registry`,
      `  uses: docker/login-action@v3`,
      `  with:`,
      `    registry: ${hostname}`,
      `    username: ${safe}`,
      `    password: \${{ secrets.REGISTRY_API_KEY }}`,
    ].join("\n"),
  };
}
```

- [ ] **Step 8.4: Run the tests, confirm all pass**

```bash
cd frontend && npm run test -- --run credential-snippets
```

Expected: all 6 tests pass.

- [ ] **Step 8.5: Commit**

```bash
git add frontend/src/lib/credential-snippets.ts frontend/src/lib/__tests__/credential-snippets.test.ts
git commit -m "feat(frontend): buildSnippets pure helper + tests (FUT-002)"
```

---

## Task 9: FE — `HelpersPanel` component

**Files:**
- Create: `frontend/src/components/access/HelpersPanel.tsx`
- Create: `frontend/src/components/access/__tests__/HelpersPanel.test.tsx`

- [ ] **Step 9.1: Write the failing component test**

```typescript
import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { HelpersPanel } from "../HelpersPanel";

function renderWithClient(ui: React.ReactElement) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>);
}

describe("HelpersPanel", () => {
  it("renders the heading + does NOT render the amber preview banner", () => {
    renderWithClient(<HelpersPanel />);
    expect(
      screen.getByRole("heading", { name: /credential helpers/i })
    ).toBeInTheDocument();
    // The preview banner is gone now that the surface is live.
    expect(
      screen.queryByText(/Sprint 11.*FUT-002/i)
    ).not.toBeInTheDocument();
  });

  it("renders a loading state while data fetches", () => {
    renderWithClient(<HelpersPanel />);
    expect(screen.getByRole("status")).toBeInTheDocument();
  });
});
```

- [ ] **Step 9.2: Run the test, confirm it fails on missing component**

```bash
cd frontend && npm run test -- --run HelpersPanel
```

Expected: `Failed to load module '../HelpersPanel'`.

- [ ] **Step 9.3: Implement the component**

```typescript
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
    "docker login"
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

  if (isLoading) {
    return (
      <div role="status" className="text-sm text-[var(--color-fg-muted)]">
        Loading credential helpers&hellip;
      </div>
    );
  }

  if (hasError || !registryInfo.data) {
    return (
      <div role="alert" className="text-sm text-red-600">
        Failed to load credential helpers. Try refreshing the page.
      </div>
    );
  }

  const activeSA = serviceAccounts.data?.find((sa) => sa.id === activeSAId);
  const snippets = activeSA
    ? buildSnippets({
        hostname: registryInfo.data.registry_host,
        saName: activeSA.name,
      })
    : null;

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
    </div>
  );
}
```

- [ ] **Step 9.4: Run the test, confirm both pass**

```bash
cd frontend && npm run test -- --run HelpersPanel
```

Expected: both tests pass.

- [ ] **Step 9.5: Commit**

```bash
git add frontend/src/components/access/HelpersPanel.tsx frontend/src/components/access/__tests__/HelpersPanel.test.tsx
git commit -m "feat(frontend): live HelpersPanel component (FUT-002)"
```

---

## Task 10: Swap the route + delete the preview

**Files:**
- Modify: `frontend/src/routes/_authenticated.api-keys.helpers.tsx`
- Delete: `frontend/src/components/access/previews/HelpersPreview.tsx`

- [ ] **Step 10.1: Rewrite the route file**

Replace the entire content with:

```typescript
import { createFileRoute } from "@tanstack/react-router";
import { HelpersPanel } from "@/components/access/HelpersPanel";

// /api-keys/helpers — live credential-helpers surface (FUT-002).
// The layout-level admin gate in AccessSubNav hides this link for non-admins;
// no additional beforeLoad guard is required here.
export const Route = createFileRoute("/_authenticated/api-keys/helpers")({
  component: HelpersPanel,
});
```

- [ ] **Step 10.2: Delete the preview component**

```bash
rm frontend/src/components/access/previews/HelpersPreview.tsx
```

- [ ] **Step 10.3: Check for orphan imports**

```bash
grep -rn "HelpersPreview" frontend/src 2>&1
```

Expected: zero results. If any test file still references it, drop those tests (they're testing dead dummy data) or rewrite them against `HelpersPanel`.

- [ ] **Step 10.4: Commit**

```bash
git add frontend/src/routes/_authenticated.api-keys.helpers.tsx frontend/src/components/access/previews/HelpersPreview.tsx
git commit -m "feat(frontend): /api-keys/helpers swaps preview for live panel (FUT-002)"
```

---

## Task 11: Tracker hygiene

**Files:**
- Modify: `status-tracker.md`
- Modify: `futures.md`

- [ ] **Step 11.1: Add a REM-021 entry to `status-tracker.md`**

In `status-tracker.md`, find the closing `---` separator that ends the `### REM-013` section and insert the new block immediately after it (so REM-021 sits between REM-013 and REM-014):

```markdown
### REM-021 — FUT-002 Credential helpers (in flight)

**Affects:** `services/management` (new `/api/v1/registry-info` route + `PLATFORM_HOST` env), `frontend` (new `HelpersPanel` replacing the preview).

**Status:** IN FLIGHT on `feat/fut-002-credential-helpers`. Smallest of the FUT-001..FUT-004 batch (`/api-keys/helpers` going from preview to live). Spec: `docs/superpowers/specs/2026-06-30-api-keys-tier2-backend-design.md`. No DB / proto change.

**Plan:** `docs/superpowers/plans/2026-06-30-fut-002-credential-helpers.md`.

**On merge:** remove this entry; append a resolution row to `status.md`.

---

```

- [ ] **Step 11.2: Stub out FUT-002 in `futures.md`**

In `futures.md`, find the `### FUT-002: Credential helpers` section heading and replace its body (everything between this heading and the next `### FUT-003:` heading, exclusive) with:

```markdown
### FUT-002: Credential helpers (docker login / k8s YAML / terraform / GHA snippets) — Sprint 11

**DONE — see `status.md` (REM-021).** Design history preserved below for context.

<details>
<summary>Original FUT-002 design (pre-implementation)</summary>

- **Why:** Operators copy-paste credentials into CI configs and get them wrong.
  Auto-generated, copy-ready snippets reduce support burden.
- **What:** `/api-keys` Helpers tab (preview surface already shipped) renders
  per-format snippets: `docker login` command, Kubernetes imagePullSecret YAML,
  Terraform `docker_registry_image` block, GitHub Actions step. All snippets
  reference the workspace's actual registry hostname and the selected service
  account. No new backend RPCs needed — purely frontend rendering against
  existing `/api/v1/workspace/me` data.

</details>
```

- [ ] **Step 11.3: Commit**

```bash
git add status-tracker.md futures.md
git commit -m "chore(trackers): REM-021 FUT-002 in-flight entry + futures.md stub"
```

---

## Task 12: Local CI gate

- [ ] **Step 12.1: Backend gate**

```bash
cd services/management && go vet ./... && go build ./... && go test ./...
```

Expected: every command exits 0; no test failures.

- [ ] **Step 12.2: Frontend gate (all 4 CI equivalents per CLAUDE.md §15)**

```bash
cd frontend && npm run lint && npm run typecheck && npm run test && npm run build
```

Expected: every command exits 0; lint 0 errors; typecheck 0 errors; all vitest pass; build completes.

- [ ] **Step 12.3: Spec-lint regression check**

```bash
cd /c/Users/Athelos/Desktop/claude/image-registry && go build -C tools/spec-lint -o spec-lint.exe . && ./tools/spec-lint/spec-lint.exe .
rm tools/spec-lint/spec-lint.exe
```

Expected: `spec-lint: all 13 rules passed`. No new rules to add (this PR doesn't make new load-bearing CLAUDE.md claims).

---

## Task 13: 3-agent review batch (BEFORE `gh pr create`)

Per `memory/feedback_review_agents_batch.md` and the workflow rule in `memory/current_sprint_status.md` — these fire in parallel as one batch.

- [ ] **Step 13.1: Spawn security-agent + qa-agent + code-review-agent in a single message**

Use the `Agent` tool three times in one assistant message (one per agent). Each agent gets:
- The PR branch name (`feat/fut-002-credential-helpers`)
- The spec doc path (`docs/superpowers/specs/2026-06-30-api-keys-tier2-backend-design.md`)
- The plan doc path (`docs/superpowers/plans/2026-06-30-fut-002-credential-helpers.md`)
- The scope summary: "FUT-002 only — lift /api-keys/helpers from preview to live; new BFF endpoint + new FE panel + tracker hygiene. No DB. No proto."

- [ ] **Step 13.2: Fold must-fixes inline; defer should-fixes to follow-ups**

Per `memory/feedback_review_pace.md`: subagent-driven, skip code-quality reviews, keep spec compliance, fix small must-fixes inline, accept should-fixes as follow-ups.

If any agent returns FAIL with a blocker: fix it on the same branch, amend or add a fix commit, re-run that one agent.

If any agent returns PASS WITH NITS: log the nits in `status-tracker.md` as REM-021-followup items, do not block the PR.

---

## Task 14: Open the PR

- [ ] **Step 14.1: Push the branch**

```bash
git push -u origin feat/fut-002-credential-helpers
```

- [ ] **Step 14.2: Create the PR**

```bash
gh pr create --title "feat(management,frontend): FUT-002 credential helpers — lift /api-keys/helpers from preview to live" --body "$(cat <<'EOF'
## Summary

Lifts the `/api-keys/helpers` surface from the FE-API-048 amber preview to live:

- **New BFF endpoint** `GET /api/v1/registry-info` returns `{registry_host, supports_oci_v1_1}` from the new `PLATFORM_HOST` env on `services/management`. Mirrors `handleDeploymentInfo`'s unauthenticated posture; required in production.
- **New FE panel** `HelpersPanel` replaces `HelpersPreview`. Composes copy-paste-ready `docker login` / k8s Secret / Terraform / GHA snippets via the pure `buildSnippets()` helper, parameterised on the operator's selected service account + the deployment registry hostname.
- **Tracker hygiene:** `status-tracker.md` gains REM-021 in-flight; `futures.md` FUT-002 section collapses to a "DONE — see status.md" stub with the original design preserved as a `<details>` block.

Smallest of the FUT-001..FUT-004 batch (see [`docs/superpowers/specs/2026-06-30-api-keys-tier2-backend-design.md`](../blob/feat/fut-002-credential-helpers/docs/superpowers/specs/2026-06-30-api-keys-tier2-backend-design.md)). No schema change. No proto change.

## Test plan

- [ ] Reviewer reads the spec + plan and confirms the implementation matches.
- [ ] `services/management` go test ./... passes.
- [ ] `frontend` lint + typecheck + test + build all green (CLAUDE.md §15 gate).
- [ ] Spec-lint passes — no new aspirational claims introduced.
- [ ] Manual: spin up the dev stack, hit `/api-keys/helpers`, pick a service account, verify each of the 4 snippet formats contains the correct hostname + SA name + no `<REGISTRY_HOST>` placeholders.
- [ ] Manual: `curl http://localhost:8080/api/v1/registry-info` returns the expected body.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 14.3: Report the PR URL**

The output of `gh pr create` will print the URL. Hand it back to the user.

---

## Notes for the executor

- **Memory rules in effect** (see `~/.claude/projects/.../memory/`):
  - `feedback_code_comments.md`: every new file gets a top-of-file comment + per-function doc string. Already encoded in the code blocks above.
  - `feedback_git_workflow.md`: feature branch → PR → main. Branch already created.
  - `feedback_review_pace.md`: small must-fixes inline; should-fixes as follow-ups in `status-tracker.md`.
  - `feedback_review_agents_batch.md`: 3-agent batch fires BEFORE `gh pr create`.
  - `feedback_ci_pipeline_gate.md`: all 4 FE CI equivalents required, not just typecheck + vitest.

- **If a step's expected output diverges from reality:** stop, read the actual output, fix the underlying issue (or update the plan if reality is correct and the plan was wrong). Don't proceed past a divergence.

- **If a tool call fails:** the harness automatically tracks file state; don't re-read files you just edited to "verify." Trust the Edit / Write success message.

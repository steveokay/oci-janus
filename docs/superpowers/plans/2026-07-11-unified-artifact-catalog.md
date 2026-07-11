# Unified Artifact Catalog Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fold Helm charts into the environments → repository → tag catalog as a filterable lens — per-repo artifact-type badges + type filter chips — and retire `/helm` as a separate catalog.

**Architecture:** Backend adds a per-repo `artifact_types` list (computed in Go from manifest config/media types via the existing `deriveArtifactType`, single source of truth) to the repo-list response, plus per-org type counts on the environments-summary aggregate. Frontend adds a Type column + filter chips to the per-env list (reusing the existing `?artifact_type=` filter) and a type split on the org cards, then redirects `/helm` into the unified structure.

**Tech Stack:** Go 1.25.11, `buf`, `pgx/v5`, React + TypeScript, TanStack Router/Query, Vitest.

**Spec:** `docs/superpowers/specs/2026-07-11-unified-artifact-catalog-design.md`. **Branch:** `feat/unified-artifact-catalog` (already checked out).

---

## Decisions locked before tasks

- **`artifact_types` computed in Go**, reusing `deriveArtifactType(configMediaType, mediaType)` (`services/metadata/internal/repository/repository.go:988`) — one extra `manifests` query per `ListRepositories` call keyed by the result repo IDs, aggregated to a sorted unique `[]string` per repo. No SQL re-implementation of the type mapping.
- **Filter chips reuse the existing `?artifact_type=` param** already on `GET /api/v1/repositories` — no new filter plumbing.
- **Badge colors (approved mockup): image = cyan, chart = amber.** A new shared `ArtifactTypeBadge` component owns this. The repo Type column shows a badge for **every** type the repo contains (so image gets a badge, unlike the dense tag-panel pill which hides image). The existing tag-panel pills are left unchanged this feature (see "Open decision" at handoff).
- **v1 chips: All / Images / Charts** only. `artifact_types` may also contain `signature`/`sbom`/`other`; those render with a neutral badge in the Type column but have no chip.
- **Org next proto fields:** `Repository.artifact_types = 14`; `OrgSummary.image_repo_count = 6`, `helm_repo_count = 7`.

## File Structure

**Backend**
- `proto/metadata/v1/metadata.proto` — `Repository.artifact_types`, `OrgSummary.image_repo_count`/`helm_repo_count`. Regenerate `proto/gen/go/metadata/v1/*`.
- `services/metadata/internal/repository/repository.go` — populate `artifact_types` in `ListRepositories`; add the two counts to `ListOrgSummaries`.
- `services/management/internal/handler/handler.go` — `RepoResponse.ArtifactTypes` + `repoToResponse`; `OrgSummaryResponse` counts + `handleListOrgs`.
- Tests: metadata integration + management handler tests.

**Frontend**
- `frontend/src/lib/api/types.ts` — `Repository.artifact_types`.
- `frontend/src/lib/api/orgs.ts` — `OrgSummary` counts.
- `frontend/src/components/repositories/artifact-type-badge.tsx` — **new** shared badge.
- `frontend/src/components/repositories/repositories-table.tsx` — Type column.
- `frontend/src/routes/_authenticated.repositories.$org.index.tsx` — filter chips.
- `frontend/src/components/orgs/org-card.tsx` — type split line.
- `frontend/src/routes/_authenticated.helm.tsx` — becomes a redirect.
- `frontend/src/components/shell/sidebar.tsx` — remove Helm item.

---

## Task 1: Proto — `artifact_types` on Repository + regenerate

**Files:**
- Modify: `proto/metadata/v1/metadata.proto`
- Modify (generated): `proto/gen/go/metadata/v1/*.go`

- [ ] **Step 1: Add the field**

In `proto/metadata/v1/metadata.proto`, in `message Repository`, after `max_cvss_score = 13;`:

```protobuf
  // Distinct artifact types present in this repo, derived per manifest
  // (image / helm / signature / sbom / other). Empty for a repo with no
  // manifests. Powers the per-repo type badge + the images/charts filter.
  repeated string artifact_types = 14;
```

- [ ] **Step 2: Regenerate**

Run: `cd proto && buf generate --template buf.gen.yaml`
Expected: `proto/gen/go/metadata/v1/metadata.pb.go` updates; `git status` shows changes under `proto/gen/go/metadata/v1/`. (Run buf from `proto/` — the repo-root `make proto` currently trips over a stale worktree copy; buf scoped to `proto/` is correct.)

- [ ] **Step 3: Verify build**

Run: `cd services/metadata && GOWORK=off go build ./...`
Expected: exit 0 (additive field, nothing breaks).

- [ ] **Step 4: Commit**

```bash
git add proto/metadata/v1/metadata.proto proto/gen/go/metadata/v1
git commit -m "feat(proto): Repository.artifact_types for the unified catalog

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Metadata — populate `artifact_types` in `ListRepositories`

**Files:**
- Modify: `services/metadata/internal/repository/repository.go`
- Test: `services/metadata/internal/testutil/integration/` (new file `artifact_types_test.go`, `//go:build integration`)

- [ ] **Step 1: Write the failing test**

Create `services/metadata/internal/testutil/integration/artifact_types_test.go` (mirror the harness in `org_summaries_test.go`: `buildRepo(t)`, `devTenantID`, seed via `GetOrCreateOrganization`/`CreateRepository`/`PutManifest`). Seed one image repo, one helm repo, one mixed repo, and assert the `ArtifactTypes` on the `ListRepositories` result:

```go
//go:build integration

package integration

import (
	"context"
	"sort"
	"testing"

	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
)

// atypeSeedManifest pushes a manifest with the given config media type so the
// repo derives the matching artifact type. Storage arg is irrelevant here.
func atypeSeedManifest(t *testing.T, repo *repository.Repository, tenantID, repoID, digest, configMediaType string) {
	t.Helper()
	// A minimal manifest whose config.mediaType drives deriveArtifactType.
	rawJSON := []byte(`{"schemaVersion":2,"config":{"mediaType":"` + configMediaType + `","size":1}}`)
	if _, err := repo.PutManifest(context.Background(), tenantID, repoID, digest,
		"application/vnd.oci.image.manifest.v1+json", rawJSON, int64(len(rawJSON))); err != nil {
		t.Fatalf("seed manifest (%s): %v", configMediaType, err)
	}
}

func TestListRepositories_artifactTypes(t *testing.T) {
	repo := buildRepo(t)
	tenantID := devTenantID
	ctx := context.Background()

	orgID, err := repo.GetOrCreateOrganization(ctx, tenantID, "atypes")
	if err != nil { t.Fatalf("org: %v", err) }

	imgRepo, _ := repo.CreateRepository(ctx, tenantID, orgID, "img", "", false, 1<<30)
	helmRepo, _ := repo.CreateRepository(ctx, tenantID, orgID, "chart", "", false, 1<<30)
	mixedRepo, _ := repo.CreateRepository(ctx, tenantID, orgID, "mixed", "", false, 1<<30)

	const imgCfg = "application/vnd.oci.image.config.v1+json"
	const helmCfg = "application/vnd.cncf.helm.config.v1+json"
	atypeSeedManifest(t, repo, tenantID, imgRepo.GetRepoId(),
		"sha256:1111111111111111111111111111111111111111111111111111111111111111", imgCfg)
	atypeSeedManifest(t, repo, tenantID, helmRepo.GetRepoId(),
		"sha256:2222222222222222222222222222222222222222222222222222222222222222", helmCfg)
	atypeSeedManifest(t, repo, tenantID, mixedRepo.GetRepoId(),
		"sha256:3333333333333333333333333333333333333333333333333333333333333333", imgCfg)
	atypeSeedManifest(t, repo, tenantID, mixedRepo.GetRepoId(),
		"sha256:4444444444444444444444444444444444444444444444444444444444444444", helmCfg)

	repos, err := repo.ListRepositories(ctx, tenantID, orgID, "")
	if err != nil { t.Fatalf("list: %v", err) }

	got := map[string][]string{}
	for _, r := range repos {
		ats := append([]string(nil), r.GetArtifactTypes()...)
		sort.Strings(ats)
		got[r.GetName()] = ats
	}
	assertEq := func(name string, want []string) {
		if len(got[name]) != len(want) { t.Fatalf("%s: got %v want %v", name, got[name], want); return }
		for i := range want { if got[name][i] != want[i] { t.Errorf("%s: got %v want %v", name, got[name], want); return } }
	}
	assertEq("img", []string{"image"})
	assertEq("chart", []string{"helm"})
	assertEq("mixed", []string{"helm", "image"}) // sorted
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd services/metadata && go test -tags integration ./internal/testutil/integration/ -run TestListRepositories_artifactTypes -v`
Expected: FAIL — `ArtifactTypes` is empty (not yet populated).

- [ ] **Step 3: Implement the population**

In `services/metadata/internal/repository/repository.go`, at the END of `ListRepositories` (just before `return repos, rows.Err()`), after the scan loop has built `repos []*metadatav1.Repository`, add a second query that reuses `deriveArtifactType`. Add `sort` to the imports if not present.

```go
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Second pass: attach the distinct artifact types per repo. Computed in Go
	// via deriveArtifactType (the single source of truth) rather than an SQL
	// re-implementation of the media-type mapping. One query keyed by the
	// result repo IDs; skipped entirely when the page is empty.
	if len(repos) > 0 {
		repoIDs := make([]string, len(repos))
		for i, rp := range repos {
			repoIDs[i] = rp.GetRepoId()
		}
		typeRows, err := r.reader().Query(ctx,
			`SELECT repo_id, COALESCE(config_media_type, ''), media_type
			   FROM manifests WHERE repo_id = ANY($1::uuid[])`, repoIDs)
		if err != nil {
			return nil, fmt.Errorf("list repo artifact types: %w", err)
		}
		defer typeRows.Close()
		byRepo := make(map[string]map[string]struct{}, len(repos))
		for typeRows.Next() {
			var repoID, configMediaType, mediaType string
			if err := typeRows.Scan(&repoID, &configMediaType, &mediaType); err != nil {
				return nil, fmt.Errorf("scan repo artifact type: %w", err)
			}
			at := deriveArtifactType(configMediaType, mediaType)
			if at == "" {
				continue
			}
			if byRepo[repoID] == nil {
				byRepo[repoID] = make(map[string]struct{}, 2)
			}
			byRepo[repoID][at] = struct{}{}
		}
		if err := typeRows.Err(); err != nil {
			return nil, fmt.Errorf("iterate repo artifact types: %w", err)
		}
		for _, rp := range repos {
			set := byRepo[rp.GetRepoId()]
			if len(set) == 0 {
				continue
			}
			ats := make([]string, 0, len(set))
			for t := range set {
				ats = append(ats, t)
			}
			sort.Strings(ats)
			rp.ArtifactTypes = ats
		}
	}
	return repos, nil
}
```

Note: the original tail `return repos, rows.Err()` is replaced by the `rows.Err()` guard above + the new block + `return repos, nil`. Make sure the old `return repos, rows.Err()` line is removed (no double return).

- [ ] **Step 4: Run to verify it passes**

Run: `cd services/metadata && go test -tags integration ./internal/testutil/integration/ -run TestListRepositories_artifactTypes -v`
Expected: PASS.

- [ ] **Step 5: Regression — the existing artifact_type FILTER still works**

Run: `cd services/metadata && go test -tags integration ./internal/testutil/integration/ -run 'Repositories|OrgSummaries' 2>&1 | tail -20`
Expected: existing repo/org tests still PASS (this task only adds a field; the `?artifact_type=` EXISTS filter path is untouched). Pre-existing unrelated integration failures (pullactivity/retention main-rot) may appear — confirm they're the same as on a clean tree, not new.

- [ ] **Step 6: Commit**

```bash
git add services/metadata/internal/repository/repository.go services/metadata/internal/testutil/integration/artifact_types_test.go
git commit -m "feat(metadata): populate Repository.artifact_types in ListRepositories

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Management BFF — expose `artifact_types` on the repo JSON

**Files:**
- Modify: `services/management/internal/handler/handler.go`
- Test: `services/management/internal/handler/handler_test.go`

- [ ] **Step 1: Write the failing test**

The shared `fakeMetaServer.ListRepositories` currently streams two repos (`dev/api`, `prod/api`). Give one of them artifact types and assert the JSON. Add near `TestListRepositories_orgFilter_narrowsToOneOrg`:

```go
func TestListRepositories_exposesArtifactTypes(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Repositories []struct {
			Org           string   `json:"org"`
			ArtifactTypes []string `json:"artifact_types"`
		} `json:"repositories"`
	}
	decodeJSON(t, resp, &body)
	var dev *struct {
		Org           string   `json:"org"`
		ArtifactTypes []string `json:"artifact_types"`
	}
	for i := range body.Repositories {
		if body.Repositories[i].Org == "dev" {
			dev = &body.Repositories[i]
		}
	}
	if dev == nil {
		t.Fatalf("dev repo not in response")
	}
	if len(dev.ArtifactTypes) != 2 || dev.ArtifactTypes[0] != "image" || dev.ArtifactTypes[1] != "helm" {
		t.Errorf("dev artifact_types = %v, want [image helm]", dev.ArtifactTypes)
	}
}
```

Update the fake (in `handler_test.go`, the `ListRepositories` fake) so the `dev` repo carries `ArtifactTypes`:

```go
	_ = stream.Send(&metadatav1.Repository{RepoId: testRepoID, OrgId: testOrgID, Org: "dev", Name: "api", StorageUsed: 512, StorageQuota: 10737418240, CreatedAt: timestamppb.Now(), ArtifactTypes: []string{"image", "helm"}})
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd services/management && go test ./internal/handler/ -run TestListRepositories_exposesArtifactTypes -v`
Expected: FAIL — `artifact_types` absent from JSON (nil).

- [ ] **Step 3: Add the field + mapping**

In `handler.go`, add to `RepoResponse` (after `MaxCVSSScore`):

```go
	// Distinct artifact types the repo contains (image/helm/…) — drives the
	// per-repo type badge + the images/charts filter chips. Empty for a repo
	// with no manifests.
	ArtifactTypes []string `json:"artifact_types"`
```

In `repoToResponse` (search `func repoToResponse`), add to the returned struct:

```go
		ArtifactTypes: r.GetArtifactTypes(),
```

(Proto repeated string getter returns `[]string`; nil marshals as JSON `null` — acceptable, or normalize to `[]string{}` if the FE prefers; the FE treats missing/nil as "no badges", so leave as-is.)

- [ ] **Step 4: Run to verify it passes**

Run: `cd services/management && go test ./internal/handler/ -run TestListRepositories_exposesArtifactTypes -v`
Then the whole package: `cd services/management && GOWORK=off go test ./internal/handler/`
Expected: new test PASS; package green.

- [ ] **Step 5: Commit**

```bash
git add services/management/internal/handler/handler.go services/management/internal/handler/handler_test.go
git commit -m "feat(management): expose repo artifact_types on GET /api/v1/repositories

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Frontend — `Repository.artifact_types` type + shared `ArtifactTypeBadge`

**Files:**
- Modify: `frontend/src/lib/api/types.ts`
- Create: `frontend/src/components/repositories/artifact-type-badge.tsx`

- [ ] **Step 1: Add the type field**

In `frontend/src/lib/api/types.ts`, in `interface Repository`, after `max_cvss_score`:

```ts
  // Distinct artifact types the repo contains (image/helm/signature/sbom/other),
  // derived per manifest by the backend. Absent/empty for a repo with no
  // manifests. Drives the Type badge + the images/charts filter.
  artifact_types?: ArtifactType[];
```

(`ArtifactType` is already declared in this file.)

- [ ] **Step 2: Create the shared badge**

`frontend/src/components/repositories/artifact-type-badge.tsx`:

```tsx
import * as React from "react";
import { Box, Ship, FileSignature, FileCheck2, Package } from "lucide-react";
import { cn } from "@/lib/utils";
import type { ArtifactType } from "@/lib/api/types";

// Canonical per-artifact-type badge. image = cyan, helm = amber (the two
// first-class catalog types); signature/sbom/other get neutral treatments.
// Unlike the dense per-tag pill in tags-panel.tsx (which hides "image"),
// this badge labels every type — it's used where seeing "this is an image"
// at a glance matters (the repo Type column).
const CONFIG: Record<
  Exclude<ArtifactType, "">,
  { label: string; classes: string; Icon: React.ComponentType<{ className?: string }> }
> = {
  image: {
    label: "Image",
    classes: "border-[color:var(--color-info,#38bdf8)]/40 bg-[color:var(--color-info,#38bdf8)]/12 text-[color:var(--color-info,#38bdf8)]",
    Icon: Box,
  },
  helm: {
    label: "Helm chart",
    classes: "border-[color:var(--color-warning)]/40 bg-[color:var(--color-warning)]/12 text-[color:var(--color-warning)]",
    Icon: Ship,
  },
  signature: {
    label: "Signature",
    classes: "border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] text-[var(--color-fg-muted)]",
    Icon: FileSignature,
  },
  sbom: {
    label: "SBOM",
    classes: "border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] text-[var(--color-fg-muted)]",
    Icon: FileCheck2,
  },
  other: {
    label: "Artifact",
    classes: "border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] text-[var(--color-fg-muted)]",
    Icon: Package,
  },
};

export function ArtifactTypeBadge({ type }: { type: ArtifactType }): React.ReactElement | null {
  if (!type) return null;
  const c = CONFIG[type];
  const Icon = c.Icon;
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[10px] font-semibold",
        c.classes,
      )}
    >
      <Icon className="size-2.5" aria-hidden />
      {c.label}
    </span>
  );
}

// ArtifactTypeBadges renders one badge per type in a repo's artifact_types.
// Image + Helm first (the catalog types), then others. Renders nothing when
// the repo has no manifests.
export function ArtifactTypeBadges({ types }: { types?: ArtifactType[] }): React.ReactElement | null {
  if (!types || types.length === 0) return null;
  const order: ArtifactType[] = ["image", "helm", "signature", "sbom", "other"];
  const sorted = [...types].sort((a, b) => order.indexOf(a) - order.indexOf(b));
  return (
    <span className="flex flex-wrap gap-1.5">
      {sorted.map((t) => (
        <ArtifactTypeBadge key={t} type={t} />
      ))}
    </span>
  );
}
```

Note on colors: `--color-warning` exists (used by the tag-panel signature pill). For image, this uses `--color-info` with a `#38bdf8` cyan fallback in case that var isn't defined — **verify** whether `--color-info` exists in `frontend/src/index.css` (or the theme file); if it does, drop the fallback; if a different cyan token exists, use it. The point is image=cyan / helm=amber per the approved mockup.

- [ ] **Step 3: Typecheck**

Run: `cd frontend && npm run typecheck`
Expected: 0 errors.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/lib/api/types.ts frontend/src/components/repositories/artifact-type-badge.tsx
git commit -m "feat(frontend): Repository.artifact_types + shared ArtifactTypeBadge

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Frontend — Type column + filter chips on the per-env list

**Files:**
- Modify: `frontend/src/components/repositories/repositories-table.tsx`
- Modify: `frontend/src/routes/_authenticated.repositories.$org.index.tsx`
- Test: `frontend/src/components/repositories/__tests__/repositories-table.test.tsx` (create if absent)

- [ ] **Step 1: Add the Type column to `RepositoriesTable`**

In `repositories-table.tsx`, import the badge at the top:

```tsx
import { ArtifactTypeBadges } from "@/components/repositories/artifact-type-badge";
```

In the header row, add a `Type` head after `Visibility` (before the Storage `SortableHead`):

```tsx
            <TableHead>Type</TableHead>
```

In the `Row` component, add a cell after the Visibility cell (the `<TableCell>` with the Public/Private badge), before the Storage cell:

```tsx
      <TableCell>
        <ArtifactTypeBadges types={repo.artifact_types} />
      </TableCell>
```

- [ ] **Step 2: Add filter chips to the per-env route**

In `_authenticated.repositories.$org.index.tsx`, add a type-filter state + chip row and thread it into `useRepositories`. Add imports:

```tsx
import type { RepoArtifactFilter } from "@/lib/api/repositories";
```

Add state next to `visibility`:

```tsx
  const [artifactType, setArtifactType] = React.useState<RepoArtifactFilter>("all");
```

Pass it to the hook:

```tsx
  const {
    data, isLoading, isError, error, refetch,
    fetchNextPage, hasNextPage, isFetchingNextPage,
  } = useRepositories({ visibility, org, artifactType });
```

Render a chip row directly under `<RepositoriesToolbar ... />`:

```tsx
      <div className="flex items-center gap-1 rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] p-1 w-fit">
        {([
          { value: "all", label: "All" },
          { value: "image", label: "Images" },
          { value: "helm", label: "Charts" },
        ] as Array<{ value: RepoArtifactFilter; label: string }>).map((c) => {
          const active = artifactType === c.value;
          return (
            <button
              key={c.value}
              type="button"
              onClick={() => setArtifactType(c.value)}
              aria-pressed={active}
              className={cn(
                "rounded-sm px-3 py-1 text-xs font-medium transition-colors",
                active
                  ? "bg-[var(--color-surface-sunken)] text-[var(--color-fg)]"
                  : "text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]",
              )}
            >
              {c.label}
            </button>
          );
        })}
      </div>
```

Add `import { cn } from "@/lib/utils";` if not already imported.

- [ ] **Step 3: Write a test for the badge column**

`frontend/src/components/repositories/__tests__/repositories-table.test.tsx`:

```tsx
import { render, screen } from "@testing-library/react";
import { describe, test, expect, vi } from "vitest";
import { RepositoriesTable } from "@/components/repositories/repositories-table";
import type { Repository } from "@/lib/api/types";

// TanStack Link/useNavigate need a router context; the table only uses
// useNavigate on row click, so stub the router module.
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: React.ReactNode }) => <a>{children}</a>,
  useNavigate: () => vi.fn(),
}));

function repo(partial: Partial<Repository>): Repository {
  return {
    repo_id: "r1", org_id: "o1", org: "dev", name: "api", is_public: false,
    storage_used_bytes: 1, storage_quota_bytes: 100, created_at: "2026-07-10T00:00:00Z",
    description: "", ...partial,
  } as Repository;
}

describe("RepositoriesTable type column", () => {
  test("renders one badge per artifact type; mixed repo shows both", () => {
    render(<RepositoriesTable repositories={[repo({ artifact_types: ["image", "helm"] })]} />);
    expect(screen.getByText("Image")).toBeInTheDocument();
    expect(screen.getByText("Helm chart")).toBeInTheDocument();
  });

  test("repo with no artifact types renders no badge", () => {
    render(<RepositoriesTable repositories={[repo({ artifact_types: [] })]} />);
    expect(screen.queryByText("Image")).toBeNull();
    expect(screen.queryByText("Helm chart")).toBeNull();
  });
});
```

- [ ] **Step 4: Run tests + typecheck**

Run: `cd frontend && npm run test -- repositories-table && npm run typecheck`
Expected: table tests PASS; 0 type errors.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/components/repositories/repositories-table.tsx 'frontend/src/routes/_authenticated.repositories.$org.index.tsx' frontend/src/components/repositories/__tests__/repositories-table.test.tsx
git commit -m "feat(frontend): repo Type column + images/charts filter chips

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: Metadata — per-org type counts on `ListOrgSummaries`

**Files:**
- Modify: `proto/metadata/v1/metadata.proto` (+ regen)
- Modify: `services/metadata/internal/repository/repository.go`
- Test: `services/metadata/internal/testutil/integration/org_summaries_test.go`

- [ ] **Step 1: Add proto fields + regen**

In `message OrgSummary`, after `last_activity_at = 5;`:

```protobuf
  int64 image_repo_count = 6;  // repos in the org containing >=1 image manifest
  int64 helm_repo_count  = 7;  // repos containing >=1 helm chart (mixed counts in both)
```

Run: `cd proto && buf generate --template buf.gen.yaml` then `cd services/metadata && GOWORK=off go build ./...` (exit 0).

- [ ] **Step 2: Extend the test**

In `org_summaries_test.go` `TestListOrgSummaries`, seed a helm manifest into one dev repo and assert the counts. Add after the existing `osumSeedManifest(... devRepo1, 1024)`:

```go
	// Give dev a chart repo too so the per-type counts differ from repo_count.
	devChart := osumSeedRepo(t, repo, tenantID, devID, "chart")
	// Reuse the artifact-types seed helper shape: a helm-config manifest.
	{
		raw := []byte(`{"schemaVersion":2,"config":{"mediaType":"application/vnd.cncf.helm.config.v1+json","size":1}}`)
		if _, err := repo.PutManifest(ctx, tenantID, devChart,
			"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"application/vnd.oci.image.manifest.v1+json", raw, int64(len(raw))); err != nil {
			t.Fatalf("seed dev chart: %v", err)
		}
	}
```

Then, in the dev assertions block, add:

```go
	if got[0].GetImageRepoCount() != 1 {
		t.Errorf("dev image_repo_count = %d, want 1", got[0].GetImageRepoCount())
	}
	if got[0].GetHelmRepoCount() != 1 {
		t.Errorf("dev helm_repo_count = %d, want 1", got[0].GetHelmRepoCount())
	}
```

(dev now has: `api` with an image manifest [image], `web` empty, `chart` with a helm manifest [helm] → 1 image repo, 1 helm repo. Adjust the existing `repo_count`/`storage` asserts if the extra `chart` repo changes them: `repo_count` becomes 3.)

Update the existing dev `repo_count` assertion from 2 to **3** (api, web, chart).

- [ ] **Step 3: Run to verify it fails**

Run: `cd services/metadata && go test -tags integration ./internal/testutil/integration/ -run TestListOrgSummaries -v`
Expected: FAIL — counts are 0 (not yet computed) and/or repo_count mismatch.

- [ ] **Step 4: Implement the counts**

In `ListOrgSummaries` (`repository.go`), extend the SQL to add two `COUNT(DISTINCT ...) FILTER` columns. Pass the image + helm config-media-type sets as args via the existing `configMediaTypesFor` helper (single source of truth for the media-type lists). Replace the current query + scan:

```go
func (r *Repository) ListOrgSummaries(ctx context.Context, tenantID string) ([]*metadatav1.OrgSummary, error) {
	imageCfg := configMediaTypesFor("image")
	helmCfg := configMediaTypesFor("helm")
	const q = `
		SELECT o.id,
		       o.name,
		       COUNT(DISTINCT r.id)                         AS repo_count,
		       COALESCE(SUM(m.image_size_bytes), 0)::BIGINT AS storage_used,
		       MAX(m.created_at)                            AS last_activity,
		       COUNT(DISTINCT r.id) FILTER (WHERE m.config_media_type = ANY($2)) AS image_repos,
		       COUNT(DISTINCT r.id) FILTER (WHERE m.config_media_type = ANY($3)) AS helm_repos
		FROM organizations o
		LEFT JOIN repositories r ON r.org_id = o.id  AND r.tenant_id = o.tenant_id
		LEFT JOIN manifests    m ON m.repo_id = r.id AND m.tenant_id = o.tenant_id
		WHERE o.tenant_id = $1
		GROUP BY o.id, o.name
		ORDER BY o.name`

	rows, err := r.reader().Query(ctx, q, tenantID, imageCfg, helmCfg)
	if err != nil {
		return nil, fmt.Errorf("list org summaries: %w", err)
	}
	defer rows.Close()

	var out []*metadatav1.OrgSummary
	for rows.Next() {
		var s metadatav1.OrgSummary
		var lastActivity sql.NullTime
		if err := rows.Scan(&s.OrgId, &s.Name, &s.RepositoryCount, &s.StorageUsedBytes,
			&lastActivity, &s.ImageRepoCount, &s.HelmRepoCount); err != nil {
			return nil, fmt.Errorf("scan org summary: %w", err)
		}
		if lastActivity.Valid {
			s.LastActivityAt = timestamppb.New(lastActivity.Time)
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}
```

Note: this counts a repo as an "image repo" when it has a manifest whose `config_media_type` is an image config type. Multi-arch **index** manifests (empty config) aren't counted by this filter, but a multi-arch image always also has per-arch image-config manifests, so real image repos are still counted. This is the documented minor edge (card counts only; the per-repo `artifact_types` in Task 2 uses the full `deriveArtifactType` incl. the index fallback and is authoritative).

- [ ] **Step 5: Run to verify it passes**

Run: `cd services/metadata && go test -tags integration ./internal/testutil/integration/ -run TestListOrgSummaries -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add proto/metadata/v1/metadata.proto proto/gen/go/metadata/v1 services/metadata/internal/repository/repository.go services/metadata/internal/testutil/integration/org_summaries_test.go
git commit -m "feat(metadata): per-org image/helm repo counts on ListOrgSummaries

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: BFF + FE — org card type split

**Files:**
- Modify: `services/management/internal/handler/handler.go` + `handler_test.go`
- Modify: `frontend/src/lib/api/orgs.ts`
- Modify: `frontend/src/components/orgs/org-card.tsx`
- Test: `frontend/src/components/orgs/__tests__/org-card.test.tsx` (create)

- [ ] **Step 1: BFF — add counts to `OrgSummaryResponse` + handler (with failing test)**

In `handler_test.go`, extend the `fakeMetaServer.ListOrgSummaries` fake's dev row + `TestListOrgs_adminToken_returnsSummaries`:

```go
		{OrgId: testOrgID, Name: "dev", RepositoryCount: 3, StorageUsedBytes: 2048, LastActivityAt: timestamppb.Now(), ImageRepoCount: 2, HelmRepoCount: 1},
```

Add to the test's decoded struct + asserts:

```go
			ImageRepoCount int64 `json:"image_repo_count"`
			HelmRepoCount  int64 `json:"helm_repo_count"`
```
```go
	if body.Orgs[0].ImageRepoCount != 2 || body.Orgs[0].HelmRepoCount != 1 {
		t.Errorf("dev counts = %d/%d, want 2/1", body.Orgs[0].ImageRepoCount, body.Orgs[0].HelmRepoCount)
	}
```

Run to see it fail: `cd services/management && go test ./internal/handler/ -run TestListOrgs -v` (counts are 0).

In `handler.go`, add to `OrgSummaryResponse`:

```go
	ImageRepoCount int64 `json:"image_repo_count"`
	HelmRepoCount  int64 `json:"helm_repo_count"`
```

In `handleListOrgs`, set them in the row build:

```go
			ImageRepoCount: o.GetImageRepoCount(),
			HelmRepoCount:  o.GetHelmRepoCount(),
```

Run to pass: `cd services/management && go test ./internal/handler/ -run TestListOrgs -v` → PASS.

- [ ] **Step 2: FE — add counts to the `OrgSummary` type**

In `frontend/src/lib/api/orgs.ts`, extend `OrgSummary`:

```ts
  image_repo_count?: number;
  helm_repo_count?: number;
```

- [ ] **Step 3: FE — render the split on `OrgCard` (with failing test)**

Create `frontend/src/components/orgs/__tests__/org-card.test.tsx`:

```tsx
import { render, screen } from "@testing-library/react";
import { describe, test, expect, vi } from "vitest";
import { OrgCard } from "@/components/orgs/org-card";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: React.ReactNode }) => <a>{children}</a>,
}));

describe("OrgCard type split", () => {
  test("shows image + chart counts when present", () => {
    render(<OrgCard org={{ org_id: "o1", org: "dev", repo_count: 3, storage_used_bytes: 2048, image_repo_count: 2, helm_repo_count: 1 }} />);
    expect(screen.getByText(/2 images/i)).toBeInTheDocument();
    expect(screen.getByText(/1 chart/i)).toBeInTheDocument();
  });

  test("omits the split when there are no charts and no images", () => {
    render(<OrgCard org={{ org_id: "o1", org: "dev", repo_count: 0, storage_used_bytes: 0 }} />);
    expect(screen.queryByText(/images/i)).toBeNull();
  });
});
```

Run to fail: `cd frontend && npm run test -- org-card` (no split rendered).

In `org-card.tsx`, add a split line after the `<dl>` metrics grid (before the closing `</Link>`), rendered only when either count is present/positive:

```tsx
      {((org.image_repo_count ?? 0) > 0 || (org.helm_repo_count ?? 0) > 0) ? (
        <div className="flex items-center gap-4 border-t border-[var(--color-border)] pt-3 text-xs text-[var(--color-fg-muted)]">
          {(org.image_repo_count ?? 0) > 0 ? (
            <span className="inline-flex items-center gap-1.5">
              <span className="size-2 rounded-[3px] bg-[color:var(--color-info,#38bdf8)]" aria-hidden />
              <span className="font-mono text-[var(--color-fg)]">{org.image_repo_count}</span>{" "}
              {org.image_repo_count === 1 ? "image" : "images"}
            </span>
          ) : null}
          {(org.helm_repo_count ?? 0) > 0 ? (
            <span className="inline-flex items-center gap-1.5">
              <span className="size-2 rounded-[3px] bg-[var(--color-warning)]" aria-hidden />
              <span className="font-mono text-[var(--color-fg)]">{org.helm_repo_count}</span>{" "}
              {org.helm_repo_count === 1 ? "chart" : "charts"}
            </span>
          ) : null}
        </div>
      ) : null}
```

Run to pass: `cd frontend && npm run test -- org-card` → PASS.

- [ ] **Step 4: Typecheck**

Run: `cd frontend && npm run typecheck` → 0 errors.

- [ ] **Step 5: Commit**

```bash
git add services/management/internal/handler/handler.go services/management/internal/handler/handler_test.go frontend/src/lib/api/orgs.ts frontend/src/components/orgs/org-card.tsx frontend/src/components/orgs/__tests__/org-card.test.tsx
git commit -m "feat: org card image/chart split (BFF counts + OrgCard)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 8: Retire `/helm` — redirect + remove sidebar item

**Files:**
- Modify: `frontend/src/routes/_authenticated.helm.tsx`
- Modify: `frontend/src/components/shell/sidebar.tsx`
- Test: `frontend/src/routes/__tests__/helm.redirect.route.test.tsx` (create)

- [ ] **Step 1: Write the failing redirect test**

`frontend/src/routes/__tests__/helm.redirect.route.test.tsx` — mirror the memory-router harness in `repositories.environments.route.test.tsx`. Assert that navigating to `/helm` lands on `/repositories`:

```tsx
import { describe, test, expect } from "vitest";
import { createRouter, createMemoryHistory } from "@tanstack/react-router";
import { routeTree } from "@/routeTree.gen";

describe("/helm retirement", () => {
  test("/helm redirects to /repositories", async () => {
    const history = createMemoryHistory({ initialEntries: ["/helm"] });
    const router = createRouter({ routeTree, history });
    await router.load();
    expect(router.state.location.pathname).toBe("/repositories");
  });
});
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd frontend && npm run test -- helm.redirect`
Expected: FAIL — pathname is `/helm` (still renders the page).

- [ ] **Step 3: Convert `/helm` to a redirect**

Replace the entire body of `frontend/src/routes/_authenticated.helm.tsx`:

```tsx
import { createFileRoute, redirect } from "@tanstack/react-router";

// /helm retired (unified-artifact-catalog): Helm charts are no longer a
// separate catalog. They live in the environments → repository → tag
// structure, reachable via the "Charts" filter on a repository list. This
// route now redirects to the environments overview so old links/bookmarks
// don't 404.
export const Route = createFileRoute("/_authenticated/helm")({
  beforeLoad: () => {
    throw redirect({ to: "/repositories" });
  },
});
```

- [ ] **Step 4: Remove the sidebar item**

In `frontend/src/components/shell/sidebar.tsx`, delete the `/helm` nav item (the `{ to: "/helm", label: "Helm charts", icon: Ship }` entry and its preceding comment block) from the Registry group. If `Ship` is now an unused import, remove it from the lucide-react import.

- [ ] **Step 5: Run tests + typecheck + build**

Run: `cd frontend && npm run test -- helm.redirect && npm run typecheck && npm run build`
Expected: redirect test PASS; 0 type errors; build succeeds (route tree regenerated).

- [ ] **Step 6: Commit**

```bash
git add 'frontend/src/routes/_authenticated.helm.tsx' frontend/src/components/shell/sidebar.tsx frontend/src/routes/__tests__/helm.redirect.route.test.tsx
git commit -m "feat(frontend): retire /helm — redirect + drop sidebar item

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 9: Full gate run + docs hygiene

**Files:**
- Modify: `docs/SERVICES.md`, `FE-STATUS.md`, `docs/openapi.json` + postman (regen)

- [ ] **Step 1: Backend gates**

Run:
```bash
cd services/metadata && GOWORK=off go build ./... && GOWORK=off go test ./... && GOWORK=off go test -race ./... && GOWORK=off golangci-lint run ./internal/repository/... ./internal/handler/...
cd ../management && GOWORK=off go build ./... && GOWORK=off go test -race ./... && GOWORK=off golangci-lint run ./internal/handler/...
```
Expected: all green. (Standard `go test ./...` skips the `//go:build integration` tests — those were verified per-task with Docker. `go vet` has a pre-existing `admin_tenants_test.go` copylocks warning that is NOT part of the CI lint gate — golangci-lint is; ignore the vet-only warning.)

- [ ] **Step 2: Regenerate the OpenAPI drift guard**

Run: `cd services/management && GOWORK=off make openapi`
Expected: `docs/openapi.json` + postman collection update with the new `artifact_types` / `image_repo_count` / `helm_repo_count` fields. Commit them.

- [ ] **Step 3: Frontend 4 gates (CLAUDE.md §15.1)**

Run:
```bash
cd frontend
npm run lint       # 0 errors
npm run typecheck  # 0 errors
npm run test       # all pass
npm run build      # builds clean
```

- [ ] **Step 4: Docs**

In `docs/SERVICES.md`, update the repositories route note to mention `artifact_types` on the response and the `/orgs` counts. In `FE-STATUS.md`, add an `FE-API-062` entry: unified artifact catalog (per-repo Type badge + images/charts filter chips, org-card type split, `/helm` retired → redirect). 

- [ ] **Step 5: Commit docs**

```bash
git add docs/openapi.json docs/postman/registry-management.postman_collection.json docs/SERVICES.md FE-STATUS.md
git commit -m "docs: unified artifact catalog — artifact_types/orgs counts + FE-API-062

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 6: Push + PR (when the user asks)**

```bash
git push -u origin feat/unified-artifact-catalog
```
Open a PR to `main` only when the user requests it.

---

## Self-review notes (author)

- **Spec coverage:** Slice 1 = Tasks 1–3 (per-repo artifact_types backend→BFF). Slice 2 = Tasks 4–5 (FE type field, shared badge, Type column, filter chips). Slice 3 = Tasks 6–7 (org counts backend→BFF→card split). Slice 4 = Task 8 (`/helm` redirect + sidebar). Gates/docs = Task 9. All spec sections mapped.
- **Type consistency:** proto `artifact_types` (repeated string) → `Repository.GetArtifactTypes()` → BFF `RepoResponse.ArtifactTypes []string json:"artifact_types"` → FE `Repository.artifact_types?: ArtifactType[]` → `ArtifactTypeBadges types=`. Org counts: `image_repo_count`/`helm_repo_count` consistent across proto getters (`GetImageRepoCount`/`GetHelmRepoCount`), BFF JSON, FE `OrgSummary`, card. Consistent.
- **Reused, not duplicated:** `deriveArtifactType` (per-repo types, Task 2) and `configMediaTypesFor` (org counts, Task 6) — the two existing single-sources-of-truth. No new copy of the media-type mapping.
- **Deferred (spec):** cross-env "all charts" list; first-class SBOM/signature chips.
- **Open decision surfaced at handoff:** badge colors (image=cyan/helm=amber) diverge from the tag-panel pills (helm=accent-blue, image=no pill). This plan introduces the canonical cyan/amber via a shared component for the new surfaces and leaves the tag-panel pills unchanged — reconciling them is a follow-up. Confirm the `--color-info` token exists (Task 4 Step 2) or swap the cyan token.

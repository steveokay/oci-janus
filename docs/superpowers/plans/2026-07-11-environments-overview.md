# Environments-first Repository Navigation — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the flat `/repositories` table with an environments overview (a card per org) that drills into a per-environment repository list.

**Architecture:** A new tenant-scoped `ListOrgSummaries` aggregate RPC on `registry-metadata` (mirroring the existing `GetTenantUsage` CTE pattern) feeds a new `GET /api/v1/orgs` BFF route. The frontend `/repositories` index becomes an org-card grid; a new `/repositories/$org` route renders today's `RepositoriesTable` scoped to one org via a BFF-side `?org=` filter. The `/repositories/$org/$repo` route and everything below it is untouched.

**Tech Stack:** Go 1.25.11, `buf` (proto codegen), `pgx/v5` (raw SQL), React + TypeScript, TanStack Router (file-based routes) + TanStack Query, Vitest.

**Spec:** `docs/superpowers/specs/2026-07-11-environments-overview-design.md`. Deferred cross-environment matrix is `futures.md` FUT-077.

**Branch:** `feat/environments-overview` (already checked out, based on `main`).

---

## Design decisions locked before tasks

- **Org cards data:** new unary RPC `ListOrgSummaries(tenant_id) → repeated OrgSummary{org_id, name, repository_count, storage_used_bytes, last_activity_at}`. Unpaginated (few orgs by design). Mirrors `GetTenantUsage` (`services/metadata/internal/repository/repository.go:1490`).
- **Per-env repo list:** reuse the existing `ListRepositories` path; add an optional **`?org=<name>` filter applied in the BFF** on the streamed results — the same mechanism `handleListRepositories` already uses for `visibility`. No proto/metadata change for the per-env list. (Tradeoff: metadata still streams all tenant repos; acceptable at the target scale and consistent with today's `visibility` filter. A future push-down to `org_id` is noted in the spec.)
- **Last activity:** `MAX(manifests.created_at)` per org. Nullable (an org with zero pushes yields SQL `NULL` → proto field left unset → JSON omits it → FE shows "No activity yet").
- **Storage:** `SUM(manifests.image_size_bytes)` — the exact expression `GetTenantUsage` and `repoSelectCols` already use.
- **Naming:** sidebar label stays "Repositories"; code/route/API vocabulary stays `org` (not "environment").

## File Structure

**Backend — create/modify:**
- Modify `proto/metadata/v1/metadata.proto` — add RPC + 3 messages.
- Regenerate `proto/gen/go/metadata/v1/*` (committed, via `make proto`).
- Modify `services/metadata/internal/repository/repository.go` — add `ListOrgSummaries`.
- Modify `services/metadata/internal/handler/grpc.go` — add `ListOrgSummaries` handler.
- Modify `services/management/internal/handler/handler.go` — add `handleListOrgs`, route, `OrgSummaryResponse`; add `?org=` filter to `handleListRepositories`.
- Modify `services/management/internal/handler/handler_test.go` — fake meta server method + tests.
- Test `services/metadata/internal/repository/repository_test.go` (or existing integration test file) — `ListOrgSummaries` coverage.

**Frontend — create:**
- `frontend/src/lib/api/orgs.ts` — `useOrgs` hook + `OrgSummary`/`OrgsListResponse` types.
- `frontend/src/components/orgs/org-card.tsx` — one environment card.
- `frontend/src/routes/_authenticated.repositories.$org.index.tsx` — per-env repo list.
- `frontend/src/routes/__tests__/repositories.environments.route.test.tsx` — overview tests.

**Frontend — modify:**
- `frontend/src/routes/_authenticated.repositories.index.tsx` — rewrite to org-card overview.
- `frontend/src/lib/api/repositories.ts` — add optional `org` param to `useRepositories`.
- `frontend/src/components/repositories/create-repository-dialog.tsx` — add optional `defaultOrg` prop.

---

## Task 1: Proto — `ListOrgSummaries` RPC + regenerate stubs

**Files:**
- Modify: `proto/metadata/v1/metadata.proto`
- Modify (generated): `proto/gen/go/metadata/v1/*.go`
- Modify: `services/metadata/internal/handler/grpc.go` (stub so the service still compiles)

- [ ] **Step 1: Add the RPC to the service block**

In `proto/metadata/v1/metadata.proto`, inside the `service MetadataService { ... }` block, next to `GetTenantUsage` (~line 142), add:

```protobuf
  // ListOrgSummaries returns one aggregate row per organization in the
  // tenant (repo count, total storage, last push). Unpaginated — the
  // number of orgs per tenant is small by design. Powers the frontend
  // /repositories environments overview.
  rpc ListOrgSummaries(ListOrgSummariesRequest) returns (ListOrgSummariesResponse);
```

- [ ] **Step 2: Add the three messages**

Near the `TenantUsage` message (~line 864), add:

```protobuf
message ListOrgSummariesRequest {
  string tenant_id = 1;
}

message OrgSummary {
  string org_id             = 1;
  string name               = 2;  // organizations.name (the org/namespace)
  int64  repository_count   = 3;
  int64  storage_used_bytes = 4;
  // Unset when the org has no pushed manifests yet (SQL NULL).
  google.protobuf.Timestamp last_activity_at = 5;
}

message ListOrgSummariesResponse {
  repeated OrgSummary orgs = 1;
}
```

(`google/protobuf/timestamp.proto` is already imported — `Repository.created_at` uses it.)

- [ ] **Step 3: Regenerate committed stubs**

Run: `make proto`
Expected: `proto/gen/go/metadata/v1/metadata.pb.go` + `_grpc.pb.go` update; no errors. `git status` shows changes under `proto/gen/go/metadata/v1/`.

- [ ] **Step 4: Add a compiling handler stub**

The regenerated `MetadataServiceServer` interface now requires `ListOrgSummaries`, so `services/metadata` won't build until `MetadataHandler` implements it. Add a temporary stub in `services/metadata/internal/handler/grpc.go` (real body lands in Task 3):

```go
// ListOrgSummaries is implemented in Task 3. Stub keeps the service
// compiling immediately after the proto regen.
func (h *MetadataHandler) ListOrgSummaries(ctx context.Context, req *metadatav1.ListOrgSummariesRequest) (*metadatav1.ListOrgSummariesResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ListOrgSummaries not yet implemented")
}
```

(Confirm `status` and `codes` are already imported in `grpc.go` — they are, used by `GetTenantUsage`.)

- [ ] **Step 5: Verify it builds**

Run: `cd services/metadata && GOWORK=off go build ./...`
Expected: builds cleanly (exit 0).

- [ ] **Step 6: Commit**

```bash
git add proto/metadata/v1/metadata.proto proto/gen/go/metadata/v1 services/metadata/internal/handler/grpc.go
git commit -m "feat(proto): ListOrgSummaries aggregate RPC on metadata service

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Metadata repository — `ListOrgSummaries` SQL

**Files:**
- Modify: `services/metadata/internal/repository/repository.go`
- Test: `services/metadata/internal/repository/repository_test.go` (integration lane — testcontainers Postgres)

- [ ] **Step 1: Write the failing test**

Find the existing integration test that exercises repository methods against a testcontainers Postgres (search for `GetTenantUsage` or `ListRepositories` in `*_test.go` under that package to reuse the harness/seed helpers). Add:

```go
func TestListOrgSummaries(t *testing.T) {
	repo, tenantID := newTestRepo(t) // reuse the existing harness helper name
	ctx := context.Background()

	// Seed: org "dev" with 2 repos (one with a manifest), org "prod" with 1 repo.
	devID := seedOrg(t, repo, tenantID, "dev")
	prodID := seedOrg(t, repo, tenantID, "prod")
	devRepo1 := seedRepo(t, repo, tenantID, devID, "api")
	seedRepo(t, repo, tenantID, devID, "web")        // empty repo, still counted
	seedRepo(t, repo, tenantID, prodID, "api")
	seedManifest(t, repo, tenantID, devRepo1, 1024)  // gives dev storage + activity

	got, err := repo.ListOrgSummaries(ctx, tenantID)
	if err != nil {
		t.Fatalf("ListOrgSummaries: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 orgs, got %d", len(got))
	}
	// ORDER BY name → dev first.
	if got[0].GetName() != "dev" || got[0].GetRepositoryCount() != 2 {
		t.Errorf("dev: name=%q repo_count=%d", got[0].GetName(), got[0].GetRepositoryCount())
	}
	if got[0].GetStorageUsedBytes() != 1024 {
		t.Errorf("dev storage: want 1024, got %d", got[0].GetStorageUsedBytes())
	}
	if got[0].GetLastActivityAt() == nil {
		t.Errorf("dev last_activity_at: want set, got nil")
	}
	if got[1].GetName() != "prod" || got[1].GetRepositoryCount() != 1 {
		t.Errorf("prod: name=%q repo_count=%d", got[1].GetName(), got[1].GetRepositoryCount())
	}
	if got[1].GetLastActivityAt() != nil {
		t.Errorf("prod last_activity_at: want nil (no manifests), got %v", got[1].GetLastActivityAt())
	}
}
```

If seed helpers (`seedOrg`/`seedRepo`/`seedManifest`/`newTestRepo`) don't already exist, add thin ones next to the test using `repo.pool.Exec` inserts against `organizations`/`repositories`/`manifests` (columns confirmed in `migrations/00001_initial_schema.sql`: `manifests(repo_id, tenant_id, digest, media_type, raw_json, size_bytes, created_at)` — note storage sums the `image_size_bytes` column used by `repoSelectCols`, so insert that column too).

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd services/metadata && go test ./internal/repository/ -run TestListOrgSummaries -v`
Expected: FAIL — `repo.ListOrgSummaries undefined`.

- [ ] **Step 3: Implement `ListOrgSummaries`**

Add to `services/metadata/internal/repository/repository.go` (after `GetTenantUsage`, ~line 1531). Mirror its style (CTE-free single query is fine here; use `reader()` for replica routing like `ListRepositories`):

```go
// ListOrgSummaries returns one aggregate row per organization in the
// tenant: repository count, total storage used, and the timestamp of the
// most recent manifest push (nil when the org has no manifests). Ordered
// by org name. Powers the /repositories environments overview.
//
// Storage mirrors the SUM(image_size_bytes) expression used by
// repoSelectCols + GetTenantUsage. COUNT(DISTINCT r.id) is required
// because the LEFT JOIN to manifests fans out one row per manifest; the
// LEFT JOINs also keep orgs with zero repos and repos with zero manifests
// in the result.
func (r *Repository) ListOrgSummaries(ctx context.Context, tenantID string) ([]*metadatav1.OrgSummary, error) {
	const q = `
		SELECT o.id,
		       o.name,
		       COUNT(DISTINCT r.id)                         AS repo_count,
		       COALESCE(SUM(m.image_size_bytes), 0)::BIGINT AS storage_used,
		       MAX(m.created_at)                            AS last_activity
		FROM organizations o
		LEFT JOIN repositories r ON r.org_id = o.id  AND r.tenant_id = o.tenant_id
		LEFT JOIN manifests    m ON m.repo_id = r.id AND m.tenant_id = o.tenant_id
		WHERE o.tenant_id = $1
		GROUP BY o.id, o.name
		ORDER BY o.name`

	rows, err := r.reader().Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list org summaries: %w", err)
	}
	defer rows.Close()

	var out []*metadatav1.OrgSummary
	for rows.Next() {
		var s metadatav1.OrgSummary
		var lastActivity sql.NullTime
		if err := rows.Scan(&s.OrgId, &s.Name, &s.RepositoryCount, &s.StorageUsedBytes, &lastActivity); err != nil {
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

(`sql`, `fmt`, `timestamppb` are already imported in this file — used by `ListRepositories`.)

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd services/metadata && go test ./internal/repository/ -run TestListOrgSummaries -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add services/metadata/internal/repository/repository.go services/metadata/internal/repository/repository_test.go
git commit -m "feat(metadata): ListOrgSummaries repository aggregate

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Metadata gRPC handler — wire `ListOrgSummaries` to the repo

**Files:**
- Modify: `services/metadata/internal/handler/grpc.go`
- Test: `services/metadata/internal/handler/grpc_test.go` (if a handler-test harness exists; otherwise coverage is via Task 2 + Task 4's BFF test — note that in the commit message and skip this file)

- [ ] **Step 1: Replace the stub with the real handler**

Replace the Task 1 stub in `grpc.go` with (mirrors `GetTenantUsage`, `grpc.go:1051`):

```go
// ListOrgSummaries returns per-organization aggregate rows for the tenant.
func (h *MetadataHandler) ListOrgSummaries(ctx context.Context, req *metadatav1.ListOrgSummariesRequest) (*metadatav1.ListOrgSummariesResponse, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	orgs, err := h.repo.ListOrgSummaries(ctx, req.GetTenantId())
	if err != nil {
		return nil, mapErr(err)
	}
	return &metadatav1.ListOrgSummariesResponse{Orgs: orgs}, nil
}
```

- [ ] **Step 2: Verify build + existing tests**

Run: `cd services/metadata && GOWORK=off go build ./... && go test ./internal/handler/ ./internal/repository/`
Expected: builds; tests pass.

- [ ] **Step 3: Commit**

```bash
git add services/metadata/internal/handler/grpc.go
git commit -m "feat(metadata): wire ListOrgSummaries gRPC handler to repository

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Management BFF — `GET /api/v1/orgs`

**Files:**
- Modify: `services/management/internal/handler/handler.go`
- Test: `services/management/internal/handler/handler_test.go`

- [ ] **Step 1: Write the failing test**

In `handler_test.go`, add a fake-meta method (near the existing `ListRepositories` fake, ~line 249) and a test (near `TestListRepositories_adminToken_returnsList`, ~line 1082):

```go
func (s *fakeMetaServer) ListOrgSummaries(ctx context.Context, req *metadatav1.ListOrgSummariesRequest) (*metadatav1.ListOrgSummariesResponse, error) {
	return &metadatav1.ListOrgSummariesResponse{Orgs: []*metadatav1.OrgSummary{
		{OrgId: testOrgID, Name: "dev", RepositoryCount: 3, StorageUsedBytes: 2048, LastActivityAt: timestamppb.Now()},
		{OrgId: "org-prod", Name: "prod", RepositoryCount: 1, StorageUsedBytes: 0}, // no last_activity_at
	}}, nil
}

func TestListOrgs_adminToken_returnsSummaries(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/orgs", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Orgs []struct {
			Org            string `json:"org"`
			RepoCount      int64  `json:"repo_count"`
			StorageUsed    int64  `json:"storage_used_bytes"`
			LastActivityAt *string `json:"last_activity_at"`
		} `json:"orgs"`
	}
	decodeJSON(t, resp, &body)
	if len(body.Orgs) != 2 {
		t.Fatalf("want 2 orgs, got %d", len(body.Orgs))
	}
	if body.Orgs[0].Org != "dev" || body.Orgs[0].RepoCount != 3 || body.Orgs[0].StorageUsed != 2048 {
		t.Errorf("dev row wrong: %+v", body.Orgs[0])
	}
	if body.Orgs[0].LastActivityAt == nil {
		t.Errorf("dev last_activity_at should be set")
	}
	if body.Orgs[1].LastActivityAt != nil {
		t.Errorf("prod last_activity_at should be omitted, got %v", *body.Orgs[1].LastActivityAt)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd services/management && go test ./internal/handler/ -run TestListOrgs_adminToken_returnsSummaries -v`
Expected: FAIL — 404 (route not registered) or compile error on the fake method (interface satisfied but no route).

- [ ] **Step 3: Add the JSON response struct**

In `handler.go`, near `RepoResponse` (~line 810):

```go
// OrgSummaryResponse is one environment card's worth of data on the
// /repositories overview. LastActivityAt is a pointer so an org with no
// pushed manifests omits the field (omitempty) rather than emitting a
// zero time.
type OrgSummaryResponse struct {
	OrgID          string     `json:"org_id"`
	Org            string     `json:"org"`
	RepoCount      int64      `json:"repo_count"`
	StorageUsed    int64      `json:"storage_used_bytes"`
	LastActivityAt *time.Time `json:"last_activity_at,omitempty"`
}
```

- [ ] **Step 4: Add the handler**

In `handler.go`, near `handleListRepositories` (~line 848):

```go
func (h *Handler) handleListOrgs(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())

	resp, err := h.meta.ListOrgSummaries(r.Context(), &metadatav1.ListOrgSummariesRequest{
		TenantId: tenantID,
	})
	if err != nil {
		slog.Error("ListOrgSummaries", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to list organizations")
		return
	}

	orgs := make([]OrgSummaryResponse, 0, len(resp.GetOrgs()))
	for _, o := range resp.GetOrgs() {
		row := OrgSummaryResponse{
			OrgID:       o.GetOrgId(),
			Org:         o.GetName(),
			RepoCount:   o.GetRepositoryCount(),
			StorageUsed: o.GetStorageUsedBytes(),
		}
		if ts := o.GetLastActivityAt(); ts != nil {
			t := ts.AsTime()
			row.LastActivityAt = &t
		}
		orgs = append(orgs, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"orgs": orgs})
}
```

- [ ] **Step 5: Register the route**

In the router setup, next to the repository routes (~line 358):

```go
mux.Handle("GET /api/v1/orgs", authMW(http.HandlerFunc(h.handleListOrgs)))
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `cd services/management && go test ./internal/handler/ -run TestListOrgs_adminToken_returnsSummaries -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add services/management/internal/handler/handler.go services/management/internal/handler/handler_test.go
git commit -m "feat(management): GET /api/v1/orgs environments summary endpoint

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Management BFF — `?org=` filter on `handleListRepositories`

**Files:**
- Modify: `services/management/internal/handler/handler.go`
- Test: `services/management/internal/handler/handler_test.go`

- [ ] **Step 1: Write the failing test**

The existing fake `ListRepositories` sends one repo named `myorg/myrepo` with `Org` unset. Update it to set `Org` and send two repos in different orgs so the filter is observable. Change the fake (~line 249) to:

```go
func (s *fakeMetaServer) ListRepositories(req *metadatav1.ListRepositoriesRequest, stream metadatav1.MetadataService_ListRepositoriesServer) error {
	_ = stream.Send(&metadatav1.Repository{RepoId: testRepoID, OrgId: testOrgID, Org: "dev", Name: "api", StorageUsed: 512, StorageQuota: 10737418240, CreatedAt: timestamppb.Now()})
	_ = stream.Send(&metadatav1.Repository{RepoId: "repo-2", OrgId: "org-prod", Org: "prod", Name: "api", StorageUsed: 256, StorageQuota: 10737418240, CreatedAt: timestamppb.Now()})
	return nil
}
```

Check `TestListRepositories_adminToken_returnsList` (~line 1082) — it asserts `len(repos) == 1`; update it to `== 2` since the fake now sends two. Then add:

```go
func TestListRepositories_orgFilter_narrowsToOneOrg(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories?org=prod", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Repositories []struct {
			Org  string `json:"org"`
			Name string `json:"name"`
		} `json:"repositories"`
	}
	decodeJSON(t, resp, &body)
	if len(body.Repositories) != 1 || body.Repositories[0].Org != "prod" {
		t.Errorf("want 1 prod repo, got %+v", body.Repositories)
	}
}

func TestListRepositories_invalidOrgFilter_returns400(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories?org=Bad_Org", adminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd services/management && go test ./internal/handler/ -run 'TestListRepositories' -v`
Expected: the new `orgFilter`/`invalidOrgFilter` tests FAIL (filter not implemented; invalid org returns 200 not 400).

- [ ] **Step 3: Implement the filter**

In `handleListRepositories` (~line 848), after the `visibility` param is read and before the gRPC call, add org parsing + validation (reuse the org-name rule from CLAUDE.md §7 `^[a-z0-9-]{2,64}$`):

```go
	orgFilter := r.URL.Query().Get("org")
	if orgFilter != "" && !orgNameRe.MatchString(orgFilter) {
		writeError(w, http.StatusBadRequest, "invalid org")
		return
	}
```

In the stream-receive loop, alongside the existing visibility `continue`s, add:

```go
		if orgFilter != "" && repo.GetOrg() != orgFilter {
			continue
		}
```

If a package-level org-name regexp doesn't already exist in the handler package, add near the other validators (search for `regexp.MustCompile` in the package; there is already a `validatePageToken`):

```go
// orgNameRe mirrors the org allowlist in CLAUDE.md §7.
var orgNameRe = regexp.MustCompile(`^[a-z0-9-]{2,64}$`)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd services/management && go test ./internal/handler/ -run 'TestListRepositories' -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add services/management/internal/handler/handler.go services/management/internal/handler/handler_test.go
git commit -m "feat(management): optional ?org= filter on GET /api/v1/repositories

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: Frontend API — `useOrgs` hook

**Files:**
- Create: `frontend/src/lib/api/orgs.ts`

- [ ] **Step 1: Write the hook + types**

```ts
import { useQuery } from "@tanstack/react-query";
import { apiClient } from "./client";

// One environment card's data from GET /api/v1/orgs. Mirrors the BFF
// OrgSummaryResponse. `last_activity_at` is absent when the org has no
// pushed manifests yet.
export interface OrgSummary {
  org_id: string;
  org: string;
  repo_count: number;
  storage_used_bytes: number;
  last_activity_at?: string;
}

export interface OrgsListResponse {
  orgs: OrgSummary[];
}

export const orgKeys = {
  all: ["orgs"] as const,
  list: () => [...orgKeys.all, "list"] as const,
};

// useOrgs loads the environments overview. Unpaginated — the org count
// per tenant is small by design, so a single GET returns them all.
export function useOrgs() {
  return useQuery({
    queryKey: orgKeys.list(),
    queryFn: async () => {
      const { data } = await apiClient.get<OrgsListResponse>("/orgs");
      return data;
    },
    staleTime: 15_000,
  });
}
```

- [ ] **Step 2: Typecheck**

Run: `cd frontend && npm run typecheck`
Expected: 0 errors.

- [ ] **Step 3: Commit**

```bash
git add frontend/src/lib/api/orgs.ts
git commit -m "feat(frontend): useOrgs hook for the environments overview

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: Frontend — `OrgCard` component

**Files:**
- Create: `frontend/src/components/orgs/org-card.tsx`

- [ ] **Step 1: Write the card**

Mirrors the design tokens used by `RepositoriesTable` (surface/border/accent CSS vars) and the app-wide `formatBytes`/`formatRelativeDate`/`formatAbsoluteDate` helpers.

```tsx
import * as React from "react";
import { Link } from "@tanstack/react-router";
import { Boxes, ArrowRight } from "lucide-react";
import { formatBytes, formatRelativeDate, formatAbsoluteDate } from "@/lib/format";
import type { OrgSummary } from "@/lib/api/orgs";

// OrgCard — one environment on the /repositories overview. The whole card
// is a link into that environment's repository list. Shows the three v1
// metrics: repo count, total storage, and last push (or "No activity yet"
// when the org has no manifests).
export function OrgCard({ org }: { org: OrgSummary }): React.ReactElement {
  return (
    <Link
      to="/repositories/$org"
      params={{ org: org.org }}
      className="group flex flex-col gap-4 rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-5 shadow-[var(--shadow-card)] transition-colors hover:border-[var(--color-border-strong)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/40"
    >
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <span
            className="grid size-9 shrink-0 place-items-center rounded-md bg-[var(--color-accent-subtle)] text-[var(--color-accent)]"
            aria-hidden
          >
            <Boxes className="size-5" />
          </span>
          <span className="font-display text-lg font-medium tracking-tight">
            {org.org}
          </span>
        </div>
        <ArrowRight
          className="size-4 text-[var(--color-fg-subtle)] transition-transform group-hover:translate-x-0.5"
          aria-hidden
        />
      </div>

      <dl className="grid grid-cols-3 gap-3 text-sm">
        <div className="space-y-0.5">
          <dt className="text-xs text-[var(--color-fg-subtle)]">Repositories</dt>
          <dd className="font-mono">{org.repo_count}</dd>
        </div>
        <div className="space-y-0.5">
          <dt className="text-xs text-[var(--color-fg-subtle)]">Storage</dt>
          <dd className="font-mono">{formatBytes(org.storage_used_bytes)}</dd>
        </div>
        <div className="space-y-0.5">
          <dt className="text-xs text-[var(--color-fg-subtle)]">Last push</dt>
          <dd
            className="text-[var(--color-fg-muted)]"
            title={
              org.last_activity_at
                ? formatAbsoluteDate(org.last_activity_at)
                : undefined
            }
          >
            {org.last_activity_at
              ? formatRelativeDate(org.last_activity_at)
              : "No activity yet"}
          </dd>
        </div>
      </dl>
    </Link>
  );
}
```

(Confirm `formatBytes`/`formatRelativeDate`/`formatAbsoluteDate` exports in `frontend/src/lib/format.ts` — `repositories-table.tsx` imports all three.)

- [ ] **Step 2: Typecheck**

Run: `cd frontend && npm run typecheck`
Expected: 0 errors. (The `to="/repositories/$org"` link will only fully type-resolve once Task 9 creates that route; if typecheck flags an unknown route here, complete Task 9's route file first, then re-run. Order note: it is safe to create the `$org.index.tsx` route file (Task 9 Step 1) before this step to satisfy the route type.)

- [ ] **Step 3: Commit**

```bash
git add frontend/src/components/orgs/org-card.tsx
git commit -m "feat(frontend): OrgCard for the environments overview

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 8: Frontend — per-env route `/repositories/$org` + `useRepositories({org})` + `defaultOrg`

> Done before the overview rewrite (Task 9→here reordered) so the `OrgCard` link target and the redirect destination both exist. **Do this task before Task 9's index rewrite.**

**Files:**
- Modify: `frontend/src/lib/api/repositories.ts`
- Modify: `frontend/src/components/repositories/create-repository-dialog.tsx`
- Create: `frontend/src/routes/_authenticated.repositories.$org.index.tsx`

- [ ] **Step 1: Add optional `org` param to `useRepositories`**

In `frontend/src/lib/api/repositories.ts`, extend the params + query. Update `repoKeys.list` to include org, add `org` to `UseRepositoriesParams`, and send `?org=` when set:

```ts
export const repoKeys = {
  all: ["repositories"] as const,
  list: (
    visibility: "public" | "private" | "all",
    artifactType: RepoArtifactFilter,
    org?: string,
  ) => [...repoKeys.all, "list", visibility, artifactType, org ?? ""] as const,
  detail: (org: string, repo: string) =>
    [...repoKeys.all, "detail", org, repo] as const,
};
```

```ts
interface UseRepositoriesParams {
  visibility?: RepoVisibilityFilter;
  artifactType?: RepoArtifactFilter;
  org?: string;
  perPage?: number;
}

export function useRepositories({
  visibility = "all",
  artifactType = "all",
  org,
  perPage = 25,
}: UseRepositoriesParams = {}) {
  return useInfiniteQuery({
    queryKey: repoKeys.list(visibility, artifactType, org),
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const params: Record<string, string> = { per_page: String(perPage) };
      if (visibility !== "all") params.visibility = visibility;
      if (artifactType !== "all") params.artifact_type = artifactType;
      if (org) params.org = org;
      if (pageParam) params.page_token = pageParam;
      const { data } = await apiClient.get<RepositoriesListResponse>(
        "/repositories",
        { params },
      );
      return data;
    },
    getNextPageParam: (last) =>
      last.next_page_token ? last.next_page_token : undefined,
    staleTime: 15_000,
  });
}
```

- [ ] **Step 2: Add `defaultOrg` to `CreateRepositoryDialog`**

In `create-repository-dialog.tsx`, extend props + form defaults so the per-env page can pre-select the org:

```tsx
interface CreateRepositoryDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // When set, pre-fills the Organization field (used by the per-environment
  // repo list so "New repository" lands in the environment you're viewing).
  defaultOrg?: string;
}

export function CreateRepositoryDialog({
  open,
  onOpenChange,
  defaultOrg,
}: CreateRepositoryDialogProps): React.ReactElement {
```

Change the form's `defaultValues` to seed org, and reset back to it:

```tsx
  const {
    register, handleSubmit, reset, setValue, watch,
    formState: { errors, isSubmitting },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { org: defaultOrg ?? "", name: "", is_public: false, description: "" },
  });
```

Replace the two bare `reset()` calls (in `doCreate` and the `onOpenChange` close handler) with `reset({ org: defaultOrg ?? "", name: "", is_public: false, description: "" })` so closing/reopening keeps the org seeded.

- [ ] **Step 3: Create the per-env route**

`frontend/src/routes/_authenticated.repositories.$org.index.tsx`:

```tsx
import * as React from "react";
import { createFileRoute, Link, useParams } from "@tanstack/react-router";
import { Boxes, ChevronLeft } from "lucide-react";
import { useRepositories } from "@/lib/api/repositories";
import type { RepoVisibilityFilter } from "@/lib/api/repositories";
import { RepositoriesTable } from "@/components/repositories/repositories-table";
import { RepositoriesToolbar } from "@/components/repositories/toolbar";
import { CreateRepositoryDialog } from "@/components/repositories/create-repository-dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Button } from "@/components/ui/button";

export const Route = createFileRoute("/_authenticated/repositories/$org/")({
  component: OrgRepositoriesPage,
});

// Per-environment repository list. Same table as the old flat /repositories
// view, but scoped to a single org via the BFF ?org= filter — so search and
// sort now cover just this environment instead of the whole catalogue.
function OrgRepositoriesPage(): React.ReactElement {
  const { org } = useParams({ from: "/_authenticated/repositories/$org/" });
  const [query, setQuery] = React.useState("");
  const [visibility, setVisibility] = React.useState<RepoVisibilityFilter>("all");
  const [createOpen, setCreateOpen] = React.useState(false);

  const {
    data, isLoading, isError, error, refetch,
    fetchNextPage, hasNextPage, isFetchingNextPage,
  } = useRepositories({ visibility, org });

  const flat = React.useMemo(
    () => data?.pages.flatMap((p) => p.repositories) ?? [],
    [data],
  );
  const filtered = React.useMemo(() => {
    if (!query.trim()) return flat;
    const q = query.toLowerCase();
    return flat.filter((r) => r.name.toLowerCase().includes(q));
  }, [flat, query]);

  const searchActive = query.trim() !== "";
  React.useEffect(() => {
    if (searchActive && hasNextPage && !isFetchingNextPage) void fetchNextPage();
  }, [searchActive, hasNextPage, isFetchingNextPage, fetchNextPage]);

  return (
    <div className="space-y-6">
      <header className="flex flex-col gap-1">
        <Link
          to="/repositories"
          className="flex items-center gap-1 text-xs font-medium text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]"
        >
          <ChevronLeft className="size-3.5" aria-hidden /> Environments
        </Link>
        <div className="flex items-end justify-between">
          <h1 className="font-display flex items-center gap-3 text-3xl font-medium tracking-tight">
            <Boxes className="size-7 text-[var(--color-accent)]" aria-hidden />
            {org}
          </h1>
        </div>
      </header>

      <RepositoriesToolbar
        query={query}
        onQueryChange={setQuery}
        visibility={visibility}
        onVisibilityChange={setVisibility}
        onCreateClick={() => setCreateOpen(true)}
      />

      {isError ? (
        <ErrorState
          title="Couldn't load repositories"
          description="The management API didn't answer. Verify the BFF is reachable, then retry."
          error={error}
          onRetry={() => void refetch()}
        />
      ) : !isLoading && filtered.length === 0 ? (
        <EmptyState
          icon={<Boxes className="size-5" />}
          title={query ? `No repositories match "${query}"` : `No repositories in ${org} yet`}
          description={
            query
              ? "Try a different search term, or clear the filter."
              : "Create a repository to push images into this environment."
          }
          action={
            !query ? (
              <Button onClick={() => setCreateOpen(true)}>Create a repository</Button>
            ) : (
              <Button variant="outline" onClick={() => setQuery("")}>Clear filter</Button>
            )
          }
        />
      ) : (
        <>
          <RepositoriesTable
            repositories={filtered}
            loading={isLoading}
            linkArtifactType="image"
            hasNextPage={hasNextPage}
          />
          {hasNextPage ? (
            <div className="flex justify-center pt-2">
              <Button
                variant="outline"
                onClick={() => void fetchNextPage()}
                loading={isFetchingNextPage}
                disabled={isFetchingNextPage}
              >
                {isFetchingNextPage ? "Loading more" : "Load more"}
              </Button>
            </div>
          ) : null}
        </>
      )}

      <CreateRepositoryDialog open={createOpen} onOpenChange={setCreateOpen} defaultOrg={org} />
    </div>
  );
}
```

- [ ] **Step 4: Typecheck + build (regenerates the route tree)**

Run: `cd frontend && npm run typecheck && npm run build`
Expected: 0 type errors; build succeeds. The `prebuild`/`pretypecheck` hooks regenerate `routeTree.gen.ts` so `/repositories/$org` resolves.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/api/repositories.ts frontend/src/components/repositories/create-repository-dialog.tsx frontend/src/routes/_authenticated.repositories.\$org.index.tsx
git commit -m "feat(frontend): per-environment /repositories/\$org list

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 9: Frontend — rewrite `/repositories` as the environments overview

**Files:**
- Modify: `frontend/src/routes/_authenticated.repositories.index.tsx`
- Create: `frontend/src/routes/__tests__/repositories.environments.route.test.tsx`

- [ ] **Step 1: Write the failing route test**

`frontend/src/routes/__tests__/repositories.environments.route.test.tsx` — mirror the harness in `getting-started.redirect.route.test.tsx` (memory-history router over the real `routeTree`, mocked hooks). Mock `@/lib/api/orgs`:

```tsx
import * as React from "react";
import { render, waitFor, screen } from "@testing-library/react";
import { describe, test, expect, vi, beforeEach } from "vitest";
import { createRouter, createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { routeTree } from "@/routeTree.gen";
import type { OrgsListResponse } from "@/lib/api/orgs";

// Standard shell/icon stubs (same rationale as the getting-started route test).
vi.mock("@/components/shell/app-shell", () => ({ AppShell: ({ children }: { children: React.ReactNode }) => React.createElement("div", null, children) }));
vi.mock("@/components/shell/sidebar", () => ({ Sidebar: () => null }));
vi.mock("@/components/shell/topbar", () => ({ Topbar: () => null }));
vi.mock("@/components/shell/footer", () => ({ Footer: () => null }));
vi.mock("@/components/shell/notifications-bell", () => ({ NotificationsBell: () => null }));
vi.mock("@/components/shell/theme-toggle", () => ({ ThemeToggle: () => null }));
vi.mock("@tanstack/router-devtools", () => ({ TanStackRouterDevtools: () => null }));
vi.mock("sonner", () => ({ Toaster: () => null, toast: { success: vi.fn(), error: vi.fn() } }));

vi.mock("@/lib/auth/store", () => ({
  useAuthStore: (sel: (s: { claims: unknown; token: string | null }) => unknown) =>
    sel({ claims: { username: "u", sub: "u-1", tenant_id: "t-1", exp: 0, iat: 0, jti: "j" }, token: "tok" }),
  authStore: { getToken: () => "tok", getClaims: () => ({ sub: "u-1" }), setToken: vi.fn(), clear: vi.fn() },
}));

let mockOrgs: OrgsListResponse | undefined;
let mockLoading = false;
vi.mock("@/lib/api/orgs", () => ({
  useOrgs: () => ({ data: mockOrgs, isLoading: mockLoading, isError: false, error: undefined, refetch: vi.fn() }),
  orgKeys: { all: ["orgs"] as const, list: () => ["orgs", "list"] as const },
}));
// The per-org route imports useRepositories; stub it so mounting the tree is cheap.
vi.mock("@/lib/api/repositories", () => ({
  useRepositories: () => ({ data: undefined, isLoading: false, isError: false, error: undefined, refetch: vi.fn(), fetchNextPage: vi.fn(), hasNextPage: false, isFetchingNextPage: false }),
  useCreateRepository: () => ({ mutateAsync: vi.fn() }),
}));

async function buildRouter(path: string) {
  const history = createMemoryHistory({ initialEntries: [path] });
  const router = createRouter({ routeTree, history });
  await router.load();
  return router;
}
function renderRouter(router: Awaited<ReturnType<typeof buildRouter>>) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={qc}><RouterProvider router={router} /></QueryClientProvider>);
}

describe("/repositories environments overview", () => {
  beforeEach(() => { mockOrgs = undefined; mockLoading = false; });

  test("renders a card per org", async () => {
    mockOrgs = { orgs: [
      { org_id: "o1", org: "dev", repo_count: 3, storage_used_bytes: 2048, last_activity_at: "2026-07-10T00:00:00Z" },
      { org_id: "o2", org: "prod", repo_count: 1, storage_used_bytes: 0 },
    ] };
    const router = await buildRouter("/repositories");
    renderRouter(router);
    await waitFor(() => {
      expect(screen.getByText("dev")).toBeInTheDocument();
      expect(screen.getByText("prod")).toBeInTheDocument();
    });
  });

  test("single org redirects straight to that environment", async () => {
    mockOrgs = { orgs: [{ org_id: "o1", org: "dev", repo_count: 3, storage_used_bytes: 2048 }] };
    const router = await buildRouter("/repositories");
    renderRouter(router);
    await waitFor(() => {
      expect(router.state.location.pathname).toBe("/repositories/dev");
    });
  });

  test("zero orgs shows the empty state", async () => {
    mockOrgs = { orgs: [] };
    const router = await buildRouter("/repositories");
    renderRouter(router);
    await waitFor(() => {
      expect(screen.getByText(/No repositories yet/i)).toBeInTheDocument();
    });
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd frontend && npm run test -- repositories.environments`
Expected: FAIL (index still renders the flat table; no cards, no redirect).

- [ ] **Step 3: Rewrite the index route**

Replace the entire body of `frontend/src/routes/_authenticated.repositories.index.tsx`:

```tsx
import * as React from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { Boxes, Plus, Search } from "lucide-react";
import { useOrgs } from "@/lib/api/orgs";
import { OrgCard } from "@/components/orgs/org-card";
import { CreateRepositoryDialog } from "@/components/repositories/create-repository-dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

export const Route = createFileRoute("/_authenticated/repositories/")({
  component: EnvironmentsPage,
});

// Environments overview. Orgs are the top-level axis (operators use them as
// dev/stage/prod), so /repositories lands on a card per org and drills into
// /repositories/$org. Keeps the "Repositories" label + org vocabulary —
// "environment" is the operator's convention, not baked into the platform.
function EnvironmentsPage(): React.ReactElement {
  const navigate = useNavigate();
  const [query, setQuery] = React.useState("");
  const [createOpen, setCreateOpen] = React.useState(false);
  const { data, isLoading, isError, error, refetch } = useOrgs();

  const orgs = data?.orgs ?? [];

  // Single-org shortcut: a one-environment deployment skips the lonely
  // one-card overview and lands directly in that environment.
  React.useEffect(() => {
    if (!isLoading && !isError && orgs.length === 1) {
      void navigate({
        to: "/repositories/$org",
        params: { org: orgs[0].org },
        replace: true,
      });
    }
  }, [isLoading, isError, orgs, navigate]);

  const filtered = React.useMemo(() => {
    if (!query.trim()) return orgs;
    const q = query.toLowerCase();
    return orgs.filter((o) => o.org.toLowerCase().includes(q));
  }, [orgs, query]);

  return (
    <div className="space-y-6">
      <header className="flex flex-col gap-1">
        <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          Catalog
        </p>
        <div className="flex items-end justify-between">
          <h1 className="font-display flex items-center gap-3 text-3xl font-medium tracking-tight">
            <Boxes className="size-7 text-[var(--color-accent)]" aria-hidden />
            Repositories
          </h1>
          <p className="text-sm text-[var(--color-fg-muted)]">
            {isLoading
              ? "Loading…"
              : `${orgs.length} ${orgs.length === 1 ? "environment" : "environments"}`}
          </p>
        </div>
      </header>

      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="relative w-full max-w-sm">
          <Search
            className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-[var(--color-fg-subtle)]"
            aria-hidden
          />
          <Input
            className="pl-9"
            type="search"
            placeholder="Filter environments…"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            aria-label="Filter environments"
          />
        </div>
        <Button onClick={() => setCreateOpen(true)}>
          <Plus className="size-4" />
          New repository
        </Button>
      </div>

      {isError ? (
        <ErrorState
          title="Couldn't load environments"
          description="The management API didn't answer. Verify the BFF is reachable, then retry."
          error={error}
          onRetry={() => void refetch()}
        />
      ) : isLoading ? (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {Array.from({ length: 3 }).map((_, i) => (
            <div
              key={i}
              className="h-32 animate-pulse rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)]"
            />
          ))}
        </div>
      ) : filtered.length === 0 ? (
        <EmptyState
          icon={<Boxes className="size-5" />}
          title={query ? `No environments match "${query}"` : "No repositories yet"}
          description={
            query
              ? "Try a different search term, or clear the filter."
              : "Create your first repository to push images into this workspace."
          }
          action={
            !query ? (
              <Button onClick={() => setCreateOpen(true)}>Create a repository</Button>
            ) : (
              <Button variant="outline" onClick={() => setQuery("")}>Clear filter</Button>
            )
          }
        />
      ) : (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {filtered.map((o) => (
            <OrgCard key={o.org_id} org={o} />
          ))}
        </div>
      )}

      <CreateRepositoryDialog open={createOpen} onOpenChange={setCreateOpen} />
    </div>
  );
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd frontend && npm run test -- repositories.environments`
Expected: all 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/routes/_authenticated.repositories.index.tsx frontend/src/routes/__tests__/repositories.environments.route.test.tsx
git commit -m "feat(frontend): environments overview on /repositories

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 10: Full gate run + tracker/doc hygiene

**Files:**
- Modify: `FE-STATUS.md` (frontend surface log)
- Modify: `docs/SERVICES.md` §13 (registry-management) — document `GET /api/v1/orgs`
- Possibly modify: `frontend/src/components/repositories/toolbar.tsx` is now used only by the per-org page — no change needed (still imported there).

- [ ] **Step 1: Backend gates**

Run:
```bash
cd services/metadata && make build && make test && make lint
cd ../management && make build && make test && make lint
```
Expected: all green. If `make` is unavailable, use the direct equivalents (`GOWORK=off go build ./...`, `go test ./...`, `golangci-lint run`). Fix any lint the diff introduced.

- [ ] **Step 2: Proto gate**

Run: `make proto` then `git status`
Expected: no uncommitted regen diff (stubs already committed in Task 1). If there is a diff, commit it.

- [ ] **Step 3: Frontend — all 4 CI equivalents (CLAUDE.md §15.1)**

Run:
```bash
cd frontend
npm run lint
npm run typecheck
npm run test
npm run build
```
Expected: lint 0 errors, typecheck 0 errors, all tests pass, build succeeds.

- [ ] **Step 4: Document the new endpoint**

In `docs/SERVICES.md`, in the `registry-management` section's route list, add a line for `GET /api/v1/orgs → ListOrgSummaries` (environments overview; returns per-org repo count + storage + last activity). Add an `FE-STATUS.md` entry (next FE-API number) describing the environments-overview surface + the `/repositories/$org` route.

- [ ] **Step 5: Commit docs**

```bash
git add FE-STATUS.md docs/SERVICES.md
git commit -m "docs: environments overview — GET /api/v1/orgs + FE surface

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 6: Push + open PR**

```bash
git push -u origin feat/environments-overview
```
Then open a PR to `main` (only when the user asks — per the git-workflow preference, work stays on the branch until they request the PR/merge).

---

## Self-review notes (author)

- **Spec coverage:** org cards (T6–T9), 3 metrics repo/storage/last-activity (T2 SQL + T7 card), card click → `/repositories/$org` (T7 link + T8 route), single-org redirect (T9), empty/error states (T8/T9), `GET /api/v1/orgs` backend (T1–T4), per-env list reuse of `RepositoriesTable` (T8), naming stays "Repositories"/`org` (T9 comment), create-repo on both surfaces (T8 `defaultOrg`, T9 no-preselect). FUT-077 matrix explicitly out of scope. All covered.
- **Deferred/omitted by design:** server-side repo search (still client-side within one org, smaller now); per-env vuln posture (FUT-077-adjacent follow-up).
- **Type consistency:** proto `OrgSummary{org_id,name,repository_count,storage_used_bytes,last_activity_at}` → BFF `OrgSummaryResponse{org_id,org,repo_count,storage_used_bytes,last_activity_at}` (note the `name`→`org` and `repository_count`→`repo_count` remaps happen in `handleListOrgs`) → FE `OrgSummary{org_id,org,repo_count,storage_used_bytes,last_activity_at?}`. Consistent across T4/T6/T7.
- **Ordering caveat surfaced:** Task 8 (route) precedes Task 9 (overview) and Task 7's link so the `/repositories/$org` route type exists before it's referenced.

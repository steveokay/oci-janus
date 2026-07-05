# FUT-020 Image Promotion Implementation Plan

> **✅ SHIPPED — PR #231 (+#234 follow-up). Plan complete; canonical status in `status.md` / `FE-STATUS.md`. Task checkboxes left unticked — this is a subagent-driven execution artifact, not a live tracker.**

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan.

**Goal:** First-class image promotion primitive. `POST /repositories/{org}/{repo}/tags/{tag}/promote` atomically copies a tag's manifest to a destination `{org}/{repo}:{tag}` with digest verification + audit trail. Ships in ~1 evening.

**Architecture:** No new blob copies — a promotion is a metadata operation: (1) load source tag's manifest digest, (2) verify destination repo exists + caller has write access, (3) upsert destination tag pointing at the same digest, (4) record row in new `promotions` table for history, (5) emit `image.promoted` audit event. Storage stays deduplicated because both tags reference the same blob chain.

**Branch:** `feat/fut-020-image-promotion` (already off `main`).

**Spec anchor:** `futures.md` FUT-020.

---

## File Structure

**Created:**
- `services/metadata/migrations/20260703000002_promotions.sql` — promotions history table
- `services/metadata/internal/repository/promotions.go` — `PromoteTag` + `ListPromotions` methods
- `services/metadata/internal/repository/promotions_test.go` — integration tests
- `services/management/internal/handler/promote_tag.go` — BFF handler
- `services/management/internal/handler/promote_tag_test.go` — handler tests
- `frontend/src/lib/api/promotions.ts` — hooks: `usePromoteTag`, `usePromotionHistory`
- `frontend/src/components/repositories/PromoteTagDialog.tsx` — dialog with dest picker
- `frontend/src/components/repositories/PromotionsTab.tsx` — history table

**Modified:**
- `proto/metadata/v1/metadata.proto` — add `PromoteTag` + `ListPromotions` RPCs + messages
- `services/metadata/internal/handler/grpc.go` — 2 new handlers
- `services/management/internal/handler/handler.go` — 2 new routes
- `libs/rabbitmq/events/events.go` — `RoutingImagePromoted` const + payload
- `services/audit/internal/eventconsumer/consumer.go` — mapEvent case
- `services/auth/internal/server/server.go` — `publishImagePromoted` helper + explicit `Emit` case (FUT-003 hotfix #226 lesson)
- `frontend/src/routes/_authenticated.repositories.$org.$repo.tsx` — new Promotions tab
- `frontend/src/components/repositories/TagRow.tsx` (or wherever the tag detail row lives) — "Promote" kebab item
- `status-tracker.md` + `futures.md` — REM-027 entry + FUT-020 stub

---

## Task 1: Proto — add `PromoteTag` + `ListPromotions` RPCs

**Files:**
- Modify: `proto/metadata/v1/metadata.proto`
- Regenerate: `proto/gen/go/metadata/v1/*.pb.go`

Add messages after the existing tag messages:

```protobuf
// FUT-020 — image promotion. Atomic tag copy: destination tag ends
// pointing at the same manifest digest as the source. Storage stays
// deduplicated (both refs → same blob chain). A promotion is recorded
// in the promotions table for history + audit.
message PromoteTagRequest {
  string tenant_id       = 1;
  string src_org         = 2;
  string src_repo        = 3;
  string src_tag         = 4;
  string dst_org         = 5;
  string dst_repo        = 6;
  string dst_tag         = 7;
  string actor_user_id   = 8;
  string note            = 9;  // optional; surfaces in audit + history
}

message Promotion {
  string id                                = 1;
  string tenant_id                         = 2;
  string src_org                           = 3;
  string src_repo                          = 4;
  string src_tag                           = 5;
  string src_digest                        = 6;  // captured at promotion time
  string dst_org                           = 7;
  string dst_repo                          = 8;
  string dst_tag                           = 9;
  string dst_digest                        = 10;
  string actor_user_id                     = 11;
  string note                              = 12;
  google.protobuf.Timestamp promoted_at    = 13;
}

message ListPromotionsRequest {
  string tenant_id = 1;
  string org       = 2;  // optional filter
  string repo      = 3;  // optional filter
  int32  limit     = 4;  // default 50
}

message ListPromotionsResponse {
  repeated Promotion promotions = 1;
}
```

Add to `service MetadataService`:

```protobuf
  // FUT-020 — image promotion (atomic tag copy with digest verify).
  rpc PromoteTag(PromoteTagRequest) returns (Promotion);
  rpc ListPromotions(ListPromotionsRequest) returns (ListPromotionsResponse);
```

Regenerate:

```bash
make proto
git add proto/metadata/v1/metadata.proto proto/gen/go/metadata/
git commit -m "feat(proto/metadata): add PromoteTag + ListPromotions RPCs (FUT-020)"
```

---

## Task 2: Migration — `promotions` table

`services/metadata/migrations/20260703000002_promotions.sql`:

```sql
-- +goose Up
-- +goose StatementBegin

-- FUT-020 — image promotion history. One row per successful promotion.
-- We capture BOTH src_digest and dst_digest (currently equal — the point
-- is atomic tag copy — but the columns future-proof re-signing/
-- re-tagging flows where dst_digest could diverge).
CREATE TABLE promotions (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID        NOT NULL,
    src_org        TEXT        NOT NULL,
    src_repo       TEXT        NOT NULL,
    src_tag        TEXT        NOT NULL,
    src_digest     TEXT        NOT NULL,
    dst_org        TEXT        NOT NULL,
    dst_repo       TEXT        NOT NULL,
    dst_tag        TEXT        NOT NULL,
    dst_digest     TEXT        NOT NULL,
    actor_user_id  UUID,       -- null for CLI / bot promotions
    note           TEXT        NOT NULL DEFAULT '',
    promoted_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_promotions_tenant_time
    ON promotions (tenant_id, promoted_at DESC);
CREATE INDEX idx_promotions_dst_lookup
    ON promotions (tenant_id, dst_org, dst_repo, promoted_at DESC);

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS promotions;
-- +goose StatementEnd
```

Commit: `git add ... && git commit -m "feat(metadata): migration — promotions history (FUT-020)"`.

---

## Task 3: Repository — `PromoteTag` + `ListPromotions`

`services/metadata/internal/repository/promotions.go`:

```go
package repository

// FUT-020 — image promotion. The promote flow reads the source tag +
// upserts the destination tag inside ONE transaction so a caller
// observes either both changes or none.

// PromoteTag looks up the source manifest digest, upserts the
// destination tag pointing at the same digest, and records a
// promotions row. Returns the persisted Promotion.
//
// Errors:
//   - codes.NotFound: source tag or destination repo missing.
//   - codes.FailedPrecondition: destination repo has immutable_tags
//     AND the destination tag already exists at a different digest.
func (r *Repository) PromoteTag(ctx context.Context, in PromoteTagInput) (*metadatav1.Promotion, error) {
    // Single tx for atomicity. Rollback on any error.
    // 1. SELECT manifest_digest FROM tags WHERE tenant/org/repo/name = src... (ErrNoRows → NotFound)
    // 2. SELECT id, immutable_tags FROM repositories WHERE ... = dst_org/dst_repo (ErrNoRows → NotFound)
    // 3. If immutable_tags: SELECT manifest_digest FROM tags WHERE = dst_tag; if exists and != src_digest → FailedPrecondition
    // 4. PutTag(tenantID, dstRepoID, dst_tag, src_digest) — reuses existing PutTag helper (call it within the tx)
    // 5. INSERT INTO promotions (...) RETURNING *
    // 6. Commit
}

type PromoteTagInput struct {
    TenantID       uuid.UUID
    SrcOrg, SrcRepo, SrcTag string
    DstOrg, DstRepo, DstTag string
    ActorUserID    *uuid.UUID
    Note           string
}

// ListPromotions returns recent promotions for the tenant, filtered
// by org/repo if supplied. Ordered by promoted_at DESC. Default limit
// 50 (max 200).
func (r *Repository) ListPromotions(ctx context.Context, tenantID uuid.UUID, org, repo string, limit int32) ([]*metadatav1.Promotion, error) {
    // WHERE tenant_id = $1
    //   AND ($2 = '' OR (src_org = $2 OR dst_org = $2))
    //   AND ($3 = '' OR (src_repo = $3 OR dst_repo = $3))
    // ORDER BY promoted_at DESC LIMIT ...
}
```

Integration tests in `promotions_test.go` (build tag `integration`, testcontainers):
- `PromoteTag_HappyPath` — src exists, dst tag missing → dst created + promotion row.
- `PromoteTag_SourceMissing` → NotFound.
- `PromoteTag_DestRepoMissing` → NotFound.
- `PromoteTag_ImmutableDestExistingSameDigest` → idempotent (promotion row still recorded).
- `PromoteTag_ImmutableDestExistingDifferentDigest` → FailedPrecondition.
- `PromoteTag_AtomicRollback` — inject a failure between PutTag and INSERT → verify tag NOT created.
- `ListPromotions_FilterByOrg`.
- `ListPromotions_DefaultOrder`.

Commit: `git add ... && git commit -m "feat(metadata): PromoteTag + ListPromotions repository (FUT-020)"`.

---

## Task 4: gRPC handlers

`services/metadata/internal/handler/grpc.go` — 2 handlers as thin wrappers around the repo. Return `codes.InvalidArgument` for bad UUIDs, propagate the repo's typed error codes.

Handler tests: `PromoteTag_Success`, `PromoteTag_InvalidTenantUUID`, `PromoteTag_ForwardsFailedPrecondition`, `ListPromotions_HappyPath`.

Commit: `git add ... && git commit -m "feat(metadata): gRPC PromoteTag + ListPromotions (FUT-020)"`.

---

## Task 5: Audit event + emitter case (FUT-003 hotfix #226 lesson)

`libs/rabbitmq/events/events.go`:

```go
const RoutingImagePromoted = "image.promoted"

// ImagePromotedPayload carries the promotion details captured at
// promotion time. src_digest + dst_digest are stamped so the audit
// trail survives a future retag or delete on either end.
type ImagePromotedPayload struct {
    TenantID     string `json:"tenant_id"`
    SrcOrg       string `json:"src_org"`
    SrcRepo      string `json:"src_repo"`
    SrcTag       string `json:"src_tag"`
    SrcDigest    string `json:"src_digest"`
    DstOrg       string `json:"dst_org"`
    DstRepo      string `json:"dst_repo"`
    DstTag       string `json:"dst_tag"`
    DstDigest    string `json:"dst_digest"`
    ActorUserID  string `json:"actor_user_id,omitempty"`
    Note         string `json:"note,omitempty"`
}
```

**Where does the emit happen?** The `promotions` row insert is inside the metadata service's DB tx. Emission has to happen AFTER the tx commits so we don't publish a promotion that got rolled back. Simplest: `services/metadata` publishes directly (it already has a publisher for other events). Alternative: management BFF publishes after the metadata call returns success — simpler but decouples the emit from the write.

Pick **BFF-side emit** because metadata doesn't currently have a `RoutingImagePromoted` publisher and adding one is fresh scope. BFF calls metadata → gets `Promotion` back → publishes. Trade-off: if the publish fails, the promotion still happened (durable in DB). BFF returns 200 to the user with the promotion; audit rows follow via the eventconsumer.

`services/audit/internal/eventconsumer/consumer.go` — new `case events.RoutingImagePromoted:` maps to an `audit_events` row with `Action = "image.promoted"`, `Resource = dst_org/dst_repo:dst_tag`, `Metadata` carries the full payload.

`services/auth/internal/server/server.go` — no changes here. This event flows from BFF's publisher, not the auth emitter. But confirm: if the BFF's publisher goes through the same `rabbitMQAuditEmitter.Emit` switch, add an explicit case for `RoutingImagePromoted` to avoid the FUT-003 `default:` swallow trap. **Verify by reading services/management's publisher wiring.**

Commit: `git add ... && git commit -m "feat(audit): catalogue image.promoted event (FUT-020)"`.

---

## Task 6: BFF — promote route + history route

`services/management/internal/handler/promote_tag.go`:

- `POST /api/v1/repositories/{org}/{repo}/tags/{tag}/promote` — body `{dst_org, dst_repo, dst_tag, note?}`. AuthMW-gated + WRITE role on both src and dst repos. Actor id from JWT sub. Returns 201 + `Promotion` JSON.
- `GET /api/v1/repositories/{org}/{repo}/promotions` — returns recent promotions where src OR dst matches this org/repo. AuthMW + READ role.

After a successful `metadata.PromoteTag`, publish `RoutingImagePromoted` via the existing management-side publisher. Handle publish failure with `slog.Warn` — don't fail the response.

Handler tests: `Promote_HappyPath`, `Promote_NoWriteRoleOnDest_403`, `Promote_ImmutableDestConflict_409`, `Promote_SrcMissing_404`, `ListPromotions_HappyPath`.

Register routes in `handler.go`.

Commit: `git add ... && git commit -m "feat(management): promote + promotions BFF routes (FUT-020)"`.

---

## Task 7: FE — hooks + Vite proxy

`frontend/src/lib/api/promotions.ts`:

```typescript
export interface Promotion {
  id: string;
  src_org: string;
  src_repo: string;
  src_tag: string;
  src_digest: string;
  dst_org: string;
  dst_repo: string;
  dst_tag: string;
  dst_digest: string;
  actor_user_id: string;
  note: string;
  promoted_at: string;
}

export interface PromoteInput {
  dst_org: string;
  dst_repo: string;
  dst_tag: string;
  note?: string;
}

export function usePromoteTag(org: string, repo: string, tag: string) { /* POST */ }
export function usePromotionHistory(org: string, repo: string) { /* GET */ }
```

Vite proxy: `/api/v1/repositories` already goes to :8091 (management), so nothing new to add. Confirm by grepping `frontend/vite.config.ts`.

Commit.

---

## Task 8: FE — PromoteTagDialog + PromotionsTab + kebab item

`PromoteTagDialog.tsx` — form: destination org (autocomplete via existing `useOrgs()`), destination repo (autocomplete filtered by org), destination tag (text input, defaults to source tag), note (optional textarea). Submit → mutation → close + toast.

`PromotionsTab.tsx` — table: promoted_at | src (org/repo:tag) | dst (org/repo:tag) | actor | note. Uses `usePromotionHistory`. Empty state "No promotions yet."

Wire the tab into `_authenticated.repositories.$org.$repo.tsx` route tabs (find the existing tab pattern — probably `<TabsList>` with `<TabsTrigger>`).

Wire the kebab item on the tag row — `_authenticated.repositories.$org.$repo.tsx` renders a tag list; add a "Promote" `<DropdownMenuItem>` that opens the dialog with the tag pre-filled as source.

Tests: `PromoteTagDialog.test.tsx` (form validation + happy path + server error toast), `PromotionsTab.test.tsx` (empty state + populated state).

Commit: `git add ... && git commit -m "feat(frontend): PromoteTagDialog + Promotions tab (FUT-020)"`.

---

## Task 9: Tracker hygiene + CI gate + 3-agent batch + PR

- Add REM-027 to `status-tracker.md` (small entry — this is a simple feature).
- Update `futures.md` FUT-020 with `**DONE — see status.md (REM-027)**` stub.
- Local: `cd services/metadata && go build ./... && go test ./...` + `cd ../management && go build ./... && go test ./...` + `cd ../audit && go build ./... && go test ./...` + `cd ../auth && go build ./... && go test ./...` + `cd frontend && npm run lint && npm run typecheck && npm run test && npm run build` + `spec-lint`.
- 3-agent batch BEFORE `gh pr create` (learned lesson from FUT-003). Priority: WRITE-role gate on dest repo (security), atomic-rollback tests present (qa), event publish failure doesn't cascade (code-review).
- PR + merge + rebuild `registry-metadata` + `registry-management` + `registry-audit`.

---

## Operating rules

- Per CLAUDE.md `feedback_code_comments`.
- TDD: failing test first.
- **FUT-003 hotfix #226 lesson:** new routing key MUST have explicit case in `rabbitMQAuditEmitter.Emit` if it flows through that path.
- If BFF publisher path is separate from auth emitter, that's fine — just verify.
- Atomicity is load-bearing: the tag write + promotion row insert MUST be in ONE tx.
- Test the immutable-tags case explicitly — this is the interaction with the FUT-tag-immutability feature already shipped.

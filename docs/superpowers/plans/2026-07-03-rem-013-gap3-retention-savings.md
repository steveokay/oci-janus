# REM-013 Gap 3 — Retention Savings Implementation Plan

> **For agentic workers:** implement task-by-task, TDD, one commit per task.

**Goal:** Surface *bytes reclaimed via retention* on the dashboard storage-breakdown card, via a new `GCService.GetTenantRetentionSavings` RPC that aggregates `gc_runs.bytes_freed` for retention modes, wired through the management BFF.

**Scope note (verified 2026-07-03):** REM-013 Gap 1 (pending-delete on `ListTags`) and Gap 2's headline (run-history panel) are **already shipped**. `stats_storage.go` already surfaces the effective retention *policy* per repo (RetentionSummary/RetentionSource). The **only** unbuilt piece is the tenant-level *savings* number (bytes reclaimed). This plan builds exactly that.

**Data source:** `gc_runs` already records retention sweeps (`mode IN ('retention','retention_grace')`) with `bytes_freed` + `manifests_deleted` set by `FinalizeRetentionRun`. Hard-deletes (real reclaimed bytes) happen in the `retention_grace` run. So savings = `SUM(bytes_freed)` over succeeded retention-mode runs for the tenant.

**Architecture:** GC owns the data (gc DB). Add a read-only aggregate RPC on `GCService`. The management BFF (already holds an optional `h.gc` client) calls it as a second read inside `handleGetStorageBreakdown` (exactly like it already calls `GetTenantQuotaUsage`), adds a tenant-level field to `StorageBreakdownResponse`. FE renders it on the storage-breakdown card. **Guard `h.gc != nil`** — the GC client is optional.

---

## Task 1: GC proto — `GetTenantRetentionSavings` RPC

**Files:** `proto/gc/v1/gc.proto`; regenerate `proto/gen/go/gc/v1/**` via `make proto` (or `buf generate` from `proto/`).

- [ ] Add to `service GCService` (after `GetRetentionRunStatus`):

```proto
  // GetTenantRetentionSavings returns lifetime bytes reclaimed by retention
  // for a tenant — the SUM of bytes_freed over succeeded retention/
  // retention_grace gc_runs. Read-only aggregate for the dashboard storage
  // breakdown (REM-013 gap 3).
  rpc GetTenantRetentionSavings(GetTenantRetentionSavingsRequest) returns (TenantRetentionSavings);
```

- [ ] Add messages (place near the other retention messages; use fresh top-level messages so field numbering starts at 1):

```proto
message GetTenantRetentionSavingsRequest {
  string tenant_id = 1;
}

message TenantRetentionSavings {
  string tenant_id           = 1;
  int64  reclaimed_bytes     = 2; // SUM(bytes_freed) over succeeded retention runs
  int64  manifests_deleted   = 3; // SUM(manifests_deleted)
  int64  run_count           = 4; // number of succeeded retention runs counted
  google.protobuf.Timestamp last_run_at = 5; // completed_at of the most recent counted run (nil if none)
}
```

- [ ] Run `make proto`; confirm `proto/gen/go/gc/v1/gc.pb.go` + `gc_grpc.pb.go` regenerate and the `breaking` check passes (additive only).
- [ ] Commit: `feat(gc): GetTenantRetentionSavings proto (REM-013 gap 3)`.

## Task 2: GC repository — aggregate query

**Files:** `services/gc/internal/repository/repository.go`; test `services/gc/internal/repository/retention_savings_test.go`.

- [ ] **Test first** (testcontainers; mirror existing gc repo tests — build tag `//go:build integration` if they use `containers`). Seed `gc_runs` rows: a succeeded `retention_grace` run with `bytes_freed=1000, manifests_deleted=3`, a succeeded `retention` run with `bytes_freed=0, manifests_deleted=5`, a `failed` retention run with `bytes_freed=9999` (must be excluded), and a `full`-mode run with `bytes_freed=500` (must be excluded). Assert `GetTenantRetentionSavings` returns `reclaimed_bytes=1000, manifests_deleted=8, run_count=2`, and `last_run_at` = the most recent counted run's `completed_at`.

- [ ] Implement:

```go
// RetentionSavings is the aggregate returned by GetTenantRetentionSavings.
type RetentionSavings struct {
	ReclaimedBytes    int64
	ManifestsDeleted  int64
	RunCount          int64
	LastRunAt         *time.Time
}

// GetTenantRetentionSavings sums bytes_freed + manifests_deleted over the
// tenant's succeeded retention-mode runs. Excludes failed/queued/running runs
// and non-retention modes. (REM-013 gap 3.)
func (r *Repository) GetTenantRetentionSavings(ctx context.Context, tenantID uuid.UUID) (RetentionSavings, error) {
	var s RetentionSavings
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(bytes_freed), 0),
		       COALESCE(SUM(manifests_deleted), 0),
		       COUNT(*),
		       MAX(completed_at)
		  FROM gc_runs
		 WHERE tenant_id = $1
		   AND mode IN ('retention','retention_grace')
		   AND status = 'succeeded'`,
		tenantID,
	).Scan(&s.ReclaimedBytes, &s.ManifestsDeleted, &s.RunCount, &s.LastRunAt)
	if err != nil {
		return RetentionSavings{}, fmt.Errorf("aggregate retention savings: %w", err)
	}
	return s, nil
}
```
(Match the receiver type / pool field name to the existing repository — adjust `r.pool` if the codebase uses a different field.)

- [ ] Run the test (Docker required); confirm pass.
- [ ] Commit: `feat(gc): retention-savings aggregate query + test (REM-013 gap 3)`.

## Task 3: GC handler — RPC impl

**Files:** `services/gc/internal/handler/grpc.go`; test alongside existing handler tests.

- [ ] Add the handler method: parse `tenant_id` (`uuid.Parse`, `InvalidArgument` on bad input), call the repo method, map to the proto response (`timestamppb.New` for `last_run_at` only when non-nil). Mirror the error/wrapping style of the neighbouring GC handlers.
- [ ] Add a small handler test (fake/stub repo) asserting the mapping + the invalid-tenant path.
- [ ] `cd services/gc && go build ./... && go vet ./... && go test ./...`.
- [ ] Commit: `feat(gc): GetTenantRetentionSavings handler (REM-013 gap 3)`.

## Task 4: Management BFF — surface on storage breakdown

**Files:** `services/management/internal/handler/stats_storage.go`; test `services/management/internal/handler/handler_test.go` (or a stats_storage test).

- [ ] Add a field to `StorageBreakdownResponse`:

```go
	// RetentionReclaimedBytes is lifetime bytes reclaimed by retention for
	// this tenant (SUM over succeeded retention gc_runs). Zero when the GC
	// client is not wired or no retention has run. (REM-013 gap 3.)
	RetentionReclaimedBytes int64 `json:"retention_reclaimed_bytes"`
```

- [ ] In `handleGetStorageBreakdown`, after the quota read, add (guarding the optional client):

```go
	// REM-013 gap 3: pair the breakdown with lifetime retention savings so the
	// dashboard card can show "reclaimed via retention". The GC client is
	// optional (WithGCClient); when unwired or on error we fall back to 0,
	// which the FE renders as "—". Never fail the breakdown on this read.
	if h.gc != nil {
		if sv, svErr := h.gc.GetTenantRetentionSavings(r.Context(), &gcv1.GetTenantRetentionSavingsRequest{
			TenantId: tenantID,
		}); svErr == nil {
			out.RetentionReclaimedBytes = sv.GetReclaimedBytes()
		} else {
			slog.WarnContext(r.Context(), "GetTenantRetentionSavings (storage breakdown)", "err", svErr, "tenant_id", tenantID)
		}
	}
```
Add the `gcv1` import if not already present in this file.

- [ ] Test: extend the storage-breakdown BFF test with a fake GC client returning `reclaimed_bytes=4096`; assert the JSON carries `retention_reclaimed_bytes: 4096`. Add a nil-GC-client case asserting `0` and no panic.
- [ ] `cd services/management && go build ./... && go vet ./... && go test ./...`.
- [ ] Commit: `feat(management): surface retention savings on storage breakdown (REM-013 gap 3)`.

## Task 5: Frontend — render reclaimed bytes on the card

**Files:** `frontend/src/lib/api/stats-storage.ts`; `frontend/src/components/dashboard/storage-breakdown-card.tsx`; test alongside.

- [ ] Add to the `StorageBreakdownResponse` TS interface:

```ts
  /** REM-013 gap 3 — lifetime bytes reclaimed via retention (0 = none/unwired). */
  retention_reclaimed_bytes: number;
```

- [ ] In `storage-breakdown-card.tsx`, render a stat near the tenant used/quota line — e.g. "Reclaimed via retention: {formatBytes(retention_reclaimed_bytes)}" — showing "—" when `0`. Reuse the existing byte-formatting helper the card already uses.
- [ ] Update/extend the card's test to assert the reclaimed value renders (and "—" at 0).
- [ ] Run the 4 CI-equivalents (CLAUDE.md §15.1): `cd frontend && npm run lint && npm run typecheck && npm run test && npm run build`.
- [ ] Commit: `feat(ui): show retention savings on storage-breakdown card (REM-013 gap 3)`.

## Task 6: Tracker + verification

- [ ] Mark REM-013 Gap 3 done in `status-tracker.md` (savings shipped; the whole REM-013 entry can then move to `status.md` and out of the tracker per the workflow, since Gaps 1–3 are all resolved). Prepend a `status.md` row.
- [ ] Full gates: affected `go test ./...` (gc + management), frontend 4-CI, `make proto` clean, `breaking` additive.
- [ ] Push branch `feat/rem-013-gap3-retention-savings`, open PR.

---

## Self-Review
- **Spec coverage:** the tracker's Gap-3 deliverable ("bytes-reclaimed-via-retention column" + `GetTenantRetentionSavings` RPC + UI plumbing) → Tasks 1–5. The already-shipped policy-column part is untouched. ✅
- **Optionality:** `h.gc` nil-guard + fall-back-to-0 keeps the breakdown resilient when GC isn't wired. ✅
- **Type consistency:** `GetTenantRetentionSavings` / `TenantRetentionSavings.reclaimed_bytes` / `RetentionReclaimedBytes` / `retention_reclaimed_bytes` used consistently proto→Go→BFF→TS. ✅
- **Additive proto:** new RPC + new top-level messages only; no field renumbering. ✅

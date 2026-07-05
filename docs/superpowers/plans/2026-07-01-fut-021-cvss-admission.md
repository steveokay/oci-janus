# FUT-021 CVSS-gated Admission Policy Implementation Plan

> **✅ SHIPPED — PR #233. Plan complete; canonical status in `status.md` / `FE-STATUS.md`. Task checkboxes left unticked — this is a subagent-driven execution artifact, not a live tracker.**

**Goal:** Close the scanner → admission loop. Repos gain `max_cvss_score` (nullable INT); on pull, `services/core.GetManifest` checks the scan result and refuses if `top_cvss > threshold`. Ships in ~1 evening — the whole thing mirrors the existing `require_signature` admission code path.

**Branch:** `feat/fut-021-cvss-admission` (already off `main`).

**Spec anchor:** `futures.md` FUT-021.

**Load-bearing invariants:**
1. **Fail-OPEN** when NO scan report exists yet (don't block first pulls on a repo where scan hasn't run).
2. **Fail-CLOSED** when scan report exists but exceeds the threshold.
3. Operator opt-out — nullable column = no gate; explicit value = gate active.
4. `top_cvss` interpretation: use the highest CVSS score present in the scan report (across ALL findings). If FIXED, deprioritize? For v1, ignore fix-availability — a HIGH vuln with a fix available is still HIGH. Simpler + safer default.

---

## File Structure

**Modified:**
- `proto/metadata/v1/metadata.proto` — `Repository.max_cvss_score` field + `UpdateRepositoryCVSSPolicy` RPC
- `services/metadata/migrations/00019_repositories_max_cvss.sql` — add column
- `services/metadata/internal/repository/repository.go` — repo methods
- `services/metadata/internal/handler/grpc.go` — UpdateRepositoryCVSSPolicy handler
- `services/core/internal/service/registry.go` (or wherever `GetManifest` service logic lives) — CVSS check
- `services/core/internal/handler/http.go` — new `ErrScanScoreExceeded` → 403 mapping
- `services/management/internal/handler/repositories.go` — PATCH body accepts `max_cvss_score`
- `frontend/src/lib/api/repositories.ts` — type extension
- `frontend/src/components/repositories/RepoScanPolicySection.tsx` (NEW; adjacent to `RepoSignaturePolicySection`)
- `frontend/src/routes/_authenticated.repositories.$org.$repo.settings.tsx` — wire the new section
- `libs/rabbitmq/events/events.go` — `RoutingRepoScanPolicyChanged` const + payload
- `services/audit/internal/eventconsumer/consumer.go` — mapEvent case
- `status-tracker.md` + `futures.md` — REM-029 entry + FUT-021 stub

**No new tables.** Single column addition.

---

## Task 1: Proto — field + RPC

Add to `Repository` message:

```protobuf
  // FUT-021 — CVSS-gated admission. Nullable INT (0-100 scale, standard
  // CVSS v3.1 range). Null = no gate. Non-null = pulls fail with 403
  // when the top scan finding's CVSS score exceeds this threshold.
  google.protobuf.Int32Value max_cvss_score = 20;  // next unused field number
```

Add RPC:

```protobuf
rpc UpdateRepositoryCVSSPolicy(UpdateRepositoryCVSSPolicyRequest) returns (Repository);

message UpdateRepositoryCVSSPolicyRequest {
    string tenant_id                       = 1;
    string org                             = 2;
    string repo                            = 3;
    google.protobuf.Int32Value max_cvss_score = 4;  // nullable
    string actor_user_id                   = 5;
}
```

`make proto`. Commit: `feat(proto/metadata): CVSS admission field + RPC (FUT-021)`.

---

## Task 2: Migration

```sql
-- +goose Up
-- +goose StatementBegin

-- FUT-021 — CVSS admission gate. Null (default) = no gate; explicit
-- integer 0-100 = block pulls when the top scan CVSS score exceeds it.
-- Standard CVSS v3.1 scale (0-10 rescaled to 0-100 for finer granularity;
-- store 100*score to avoid floats).
ALTER TABLE repositories ADD COLUMN max_cvss_score INTEGER;
ALTER TABLE repositories ADD CONSTRAINT max_cvss_range CHECK (
    max_cvss_score IS NULL OR (max_cvss_score >= 0 AND max_cvss_score <= 100)
);

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
ALTER TABLE repositories DROP CONSTRAINT IF EXISTS max_cvss_range;
ALTER TABLE repositories DROP COLUMN IF EXISTS max_cvss_score;
-- +goose StatementEnd
```

Commit: `feat(metadata): migration — repositories.max_cvss_score (FUT-021)`.

---

## Task 3: Repository — read + update

`services/metadata/internal/repository/repository.go`:

- Extend the existing repo row scanning to include `max_cvss_score` (`*int32`).
- New `UpdateRepositoryCVSSPolicy(ctx, tenantID, org, repo, maxCVSS *int32) (*Repository, error)` — simple UPDATE ... WHERE ..., RETURNING *.

Integration tests: `UpdateCVSSPolicy_SetsValue`, `UpdateCVSSPolicy_ClearsValue` (nil = clear), `UpdateCVSSPolicy_RejectsBadRange` (100.max via CHECK), `GetRepository_ReturnsCVSSPolicy`.

Commit: `feat(metadata): CVSS policy repo methods (FUT-021)`.

---

## Task 4: gRPC handler

`UpdateRepositoryCVSSPolicy` thin wrapper. Validate: 0 ≤ value ≤ 100 (redundant with the CHECK constraint but produces a clean `InvalidArgument` before hitting DB).

Commit: `feat(metadata): UpdateRepositoryCVSSPolicy gRPC handler (FUT-021)`.

---

## Task 5: `services/core` — the admission gate

Extend the service layer's `GetManifest` (or wherever the pull-authorisation code lives — find where `ErrSignatureRequired` is returned; the CVSS check goes adjacent to it).

Pattern (mirror the existing signature-required check):

```go
if repo.MaxCVSSScore != nil {
    // Fail-OPEN posture: if no scan report exists yet, don't block.
    report, err := s.scanner.GetScanReport(ctx, tenantID, repo.ID, manifest.Digest)
    if errors.Is(err, service.ErrNoScanReport) {
        // First-time pull on this manifest; scan queued but not complete.
        // Log at Info so operators can see the fail-OPEN, but don't block.
        s.logger.InfoContext(ctx, "CVSS admission: no scan report yet, allowing pull",
            "tenant_id", tenantID, "repo", repo.Name, "digest", manifest.Digest)
    } else if err != nil {
        // Scanner unreachable — fail-OPEN. Operator can flip to fail-CLOSED
        // later via env var if they prefer that posture.
        s.logger.WarnContext(ctx, "CVSS admission: scanner unreachable, failing open",
            "tenant_id", tenantID, "err", err)
    } else if report.TopCVSS() > int32(*repo.MaxCVSSScore) {
        return nil, fmt.Errorf("%w: top CVSS %d exceeds threshold %d",
            ErrCVSSThresholdExceeded, report.TopCVSS(), *repo.MaxCVSSScore)
    }
}
```

Add `ErrCVSSThresholdExceeded` sentinel to the service package.

`TopCVSS()` helper on the scan report — max across all findings. If the scan report shape doesn't expose this directly, iterate findings + max the `score` field.

Tests (unit — mock the scanner):
- `AdmissionCVSS_UnderThreshold_Allows`
- `AdmissionCVSS_OverThreshold_Denies`
- `AdmissionCVSS_NoPolicy_Allows` (nil max_cvss_score → no gate)
- `AdmissionCVSS_NoScanReport_AllowsAndLogs` (fail-OPEN)
- `AdmissionCVSS_ScannerUnreachable_AllowsAndWarns`
- `AdmissionCVSS_ExactlyAtThreshold_Allows` (`>` not `>=` — a score EQUAL to the threshold is allowed)

Commit: `feat(core): CVSS-gated pull admission (FUT-021)`.

---

## Task 6: HTTP handler — 403 mapping

`services/core/internal/handler/http.go` — new branch adjacent to the `ErrSignatureRequired` check:

```go
if errors.Is(err, service.ErrCVSSThresholdExceeded) {
    ociError(w, http.StatusForbidden, "DENIED",
        "repository CVSS policy exceeded: "+err.Error())
    return
}
```

The full error message (which includes top CVSS + threshold) surfaces so CI tooling can decide next steps (waive? patch? rebuild?).

Handler test: pull path with a manifest that trips the threshold → 403 with the readable body.

Commit: `feat(core): map CVSS admission failure to 403 DENIED (FUT-021)`.

---

## Task 7: BFF — PATCH repo route accepts `max_cvss_score`

Extend the existing `PATCH /api/v1/repositories/{org}/{repo}` handler. The body already accepts `require_signature`, `immutable_tags` — add `max_cvss_score` (nullable int).

Validation: 0 ≤ value ≤ 100. `null` = clear.

Publish `RoutingRepoScanPolicyChanged` after successful update (same pattern as the signature-policy change event, if that exists; otherwise adapt from the repository-update audit event).

Commit: `feat(management): CVSS admission on repo PATCH (FUT-021)`.

---

## Task 8: Audit event

Add `RoutingRepoScanPolicyChanged` const + `RepoScanPolicyChangedPayload` in `libs/rabbitmq/events/events.go`:

```go
const RoutingRepoScanPolicyChanged = "repo.scan_policy.changed"

type RepoScanPolicyChangedPayload struct {
    TenantID     string `json:"tenant_id"`
    Org          string `json:"org"`
    Repo         string `json:"repo"`
    ActorID      string `json:"actor_id"`
    Before       *int32 `json:"before,omitempty"` // pointer so null renders
    After        *int32 `json:"after,omitempty"`
}
```

`services/audit/internal/eventconsumer/consumer.go` — mapEvent case; action = `repo.scan_policy.changed`; resource = `<org>/<repo>`.

**FUT-003 hotfix #226 lesson check:** where does this event get published? If it's via `services/management`'s existing publisher (not the auth emitter), the `Emit`-switch trap doesn't apply. Verify by grepping the management server's publisher wiring.

Commit: `feat(audit): catalogue repo.scan_policy.changed event (FUT-021)`.

---

## Task 9: FE — RepoScanPolicySection component

`frontend/src/components/repositories/RepoScanPolicySection.tsx` — mirror `RepoSignaturePolicySection` structure:

- Toggle: "Block pulls when CVSS exceeds threshold"
- When enabled: number input (0-100) + slider linking to standard severity bands (0-39 low, 40-69 medium, 70-89 high, 90-100 critical)
- Save button → `PATCH /api/v1/repositories/{org}/{repo}` with `max_cvss_score` field
- Toast on save

Wire into `_authenticated.repositories.$org.$repo.settings.tsx` right after the signature policy section.

Tests: renders + toggle + save + validation (out-of-range rejected inline).

Commit: `feat(frontend): RepoScanPolicySection (FUT-021)`.

---

## Task 10: Tracker + CI gate + 3-agent batch + PR

- Add REM-029 to `status-tracker.md`.
- Update `futures.md` FUT-021 → `**DONE — see status.md (REM-029)**` stub.
- Local BE + FE gates + spec-lint.
- 3-agent batch BEFORE `gh pr create`:
  - **security-agent** — verify fail-OPEN posture (no scan → allows), fail-CLOSED enforcement, no CVSS-info leak in error messages beyond what an authorised puller could learn from the scan report anyway.
  - **qa-agent** — coverage of all 6 admission-check branches.
  - **code-review-agent** — CVSS score interpretation (top vs sum), threshold boundary (`>` vs `>=`), consistency with signature admission.
- PR + merge + rebuild `registry-metadata` + `registry-core` + `registry-management` + `registry-audit`.

---

## Operating rules

- Per CLAUDE.md `feedback_code_comments`.
- TDD. Test the load-bearing invariants: fail-OPEN on no-scan + fail-CLOSED on exceed.
- Do NOT re-run the scan yourself in the admission path. The scanner is a separate service; the admission gate reads the already-stored result.
- If the scanner service doesn't have a `GetScanReport(tenantID, repoID, digest) → Report` gRPC yet, ADD one — small extension.

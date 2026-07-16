# Activity feed source_ip + api_key_id Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Populate the principal/API-key activity feed's `source_ip` and (authenticating) `api_key_id` columns for the registry-auth–published `service_account.*` lifecycle events.

**Architecture:** Capture the client IP (`remoteIP(r)`) and the authenticating API-key id at the auth HTTP handlers, carry them through the request `context.Context` into the audit emitter, which stamps them onto the `ServiceAccountLifecyclePayload`. The audit consumer stores `source_ip` in the existing `audit_events.actor_ip` column and keeps `api_key_id` in event metadata; `notificationFromRow` surfaces both in the notification metadata map that the auth activity reader already consumes. No proto change, no DB migration.

**Tech Stack:** Go 1.25 (`net/http`, `context`, `log/slog`), RabbitMQ (`libs/rabbitmq/events` Go structs), pgx. Cross-module: `libs/`, `services/auth`, `services/audit`.

**Spec:** `docs/superpowers/specs/2026-07-16-activity-source-ip-apikey-design.md`

**Branch:** `feat/activity-source-ip-apikey` (already created, based on the PR #377 outcome-fix branch — this plan extends `notificationMetadataMap`, which #377 also touches). Land after #377 or rebase onto it.

---

## Conventions

- Go service modules are isolated; build/test with `GOWORK=off` from the service dir (e.g. `cd services/auth && GOWORK=off go test ./...`).
- A `libs/rabbitmq/events` change is shared: per CLAUDE.md §5 the same PR must keep **both** `services/auth` and `services/audit` building + testing.
- Commit messages end with the trailer `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`. Use a bash heredoc (Git Bash / POSIX sh), never PowerShell here-strings.
- Do NOT `git stash`, and do NOT `git checkout`/`switch` other branches — this is a shared working tree.

---

## File Structure

- `services/auth/internal/service/reqmeta.go` (**new**) — request-scoped `{source_ip, api_key_id}` context carrier: `WithRequestMeta` / `RequestMetaFromContext`. One responsibility: propagate request actor-context through `ctx`.
- `services/auth/internal/service/auth.go` (**modify**) — add `KeyID uuid.UUID` (json:"-") to `Claims`.
- `services/auth/internal/handler/http.go` (**modify**) — `synthClaimsFromAPIKey` sets `KeyID`; add `HTTPHandler.auditCtx(r, claims)` helper; thread it into the SA-key handlers.
- `services/auth/internal/handler/http_service_accounts.go` (**modify**) — thread `auditCtx` into the SA CRUD + key handlers.
- `libs/rabbitmq/events/events.go` (**modify**) — add `SourceIP` / `APIKeyID` to `ServiceAccountLifecyclePayload`.
- `services/auth/internal/server/server.go` (**modify**) — `publishSALifecycle` stamps the payload from `ctx` via a pure `saLifecyclePayload(ctx, ev)` builder.
- `services/audit/internal/eventconsumer/consumer.go` (**modify**) — SA case sets `AuditEvent.ActorIP = payload.SourceIP`.
- `services/audit/internal/handler/notifications.go` (**modify**) — `rawNotificationPayload` gains `APIKeyID`; `notificationMetadataMap` surfaces `source_ip` (from the row's `ActorIP`) + `api_key_id` (from payload).
- Test files alongside each.

---

## Task 1: Request-meta context carrier + Claims.KeyID (auth)

**Files:**
- Create: `services/auth/internal/service/reqmeta.go`
- Create: `services/auth/internal/service/reqmeta_test.go`
- Modify: `services/auth/internal/service/auth.go` (add `KeyID` to `Claims`, ~line 80-119)

- [ ] **Step 1: Write the failing test** (`reqmeta_test.go`)

```go
package service

import (
	"context"
	"testing"
)

func TestRequestMeta_roundTrip(t *testing.T) {
	ctx := WithRequestMeta(context.Background(), "203.0.113.7", "key-uuid-1")
	ip, keyID := RequestMetaFromContext(ctx)
	if ip != "203.0.113.7" {
		t.Errorf("source ip = %q, want 203.0.113.7", ip)
	}
	if keyID != "key-uuid-1" {
		t.Errorf("api key id = %q, want key-uuid-1", keyID)
	}
}

func TestRequestMeta_absent_returnsEmpty(t *testing.T) {
	ip, keyID := RequestMetaFromContext(context.Background())
	if ip != "" || keyID != "" {
		t.Errorf("bare context should yield empty meta, got ip=%q keyID=%q", ip, keyID)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd services/auth && GOWORK=off go test ./internal/service/ -run TestRequestMeta -v`
Expected: compile failure — `WithRequestMeta` / `RequestMetaFromContext` undefined.

- [ ] **Step 3: Implement `reqmeta.go`**

```go
// Package service — reqmeta.go
//
// Request-scoped actor context (client source IP + authenticating API-key id)
// carried through context.Context from the HTTP handler down to the audit
// emitter. Keeping it in ctx lets the emitter stamp lifecycle events without
// threading extra params through every ServiceAccountService method.
package service

import "context"

type reqMetaKey struct{}

// requestMeta is the value stored in context. Both fields are best-effort:
// sourceIP is the trusted-proxy-resolved client IP; apiKeyID is the id of the
// API key that authenticated the request, empty for JWT/browser sessions.
type requestMeta struct {
	sourceIP string
	apiKeyID string
}

// WithRequestMeta returns a child context carrying the request's source IP and
// authenticating API-key id (either may be empty).
func WithRequestMeta(ctx context.Context, sourceIP, apiKeyID string) context.Context {
	return context.WithValue(ctx, reqMetaKey{}, requestMeta{sourceIP: sourceIP, apiKeyID: apiKeyID})
}

// RequestMetaFromContext returns (sourceIP, apiKeyID). Missing values are the
// empty string — never panics on a bare context.
func RequestMetaFromContext(ctx context.Context) (sourceIP, apiKeyID string) {
	m, _ := ctx.Value(reqMetaKey{}).(requestMeta)
	return m.sourceIP, m.apiKeyID
}
```

- [ ] **Step 4: Add `KeyID` to `Claims`** (`auth.go`, inside the `Claims` struct, after the `Amr` field near line 119)

```go
	// KeyID is the API key that authenticated this request, populated only on
	// the `Bearer key.<uuid>.<secret>` / Basic-auth API-key path (see
	// synthClaimsFromAPIKey). uuid.Nil for JWT/browser sessions. json:"-" so it
	// never touches the wire — it is an in-process field on synthesized claims,
	// not a real JWT claim.
	KeyID uuid.UUID `json:"-"`
```

Verify `auth.go` already imports `github.com/google/uuid` (it uses `uuid` elsewhere; if not, add it).

- [ ] **Step 5: Run to verify it passes**

Run: `cd services/auth && GOWORK=off go test ./internal/service/ -run TestRequestMeta -v && GOWORK=off go build ./...`
Expected: PASS + build OK.

- [ ] **Step 6: Commit**

```bash
git add services/auth/internal/service/reqmeta.go services/auth/internal/service/reqmeta_test.go services/auth/internal/service/auth.go
git commit -m "$(cat <<'EOF'
feat(auth): request-meta context carrier + Claims.KeyID for activity capture

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Event payload fields + emitter stamping (libs + auth)

**Files:**
- Modify: `libs/rabbitmq/events/events.go` (`ServiceAccountLifecyclePayload`, ~line 573)
- Modify: `services/auth/internal/server/server.go` (`publishSALifecycle`, ~line 697; add `saLifecyclePayload` builder)
- Test: `services/auth/internal/server/salifecycle_payload_test.go` (**new**)

- [ ] **Step 1: Add payload fields** (`events.go`)

In `ServiceAccountLifecyclePayload` (after `Fields map[string]any`):

```go
	// SourceIP is the client IP that initiated the mutation (trusted-proxy
	// resolved). Empty when unknown. Consumed by the audit service to populate
	// audit_events.actor_ip so the principal activity feed can show it.
	SourceIP string `json:"source_ip,omitempty"`
	// APIKeyID is the id of the API key that authenticated the request, or empty
	// for JWT/browser sessions. Surfaced on the activity feed as api_key_id.
	APIKeyID string `json:"api_key_id,omitempty"`
```

- [ ] **Step 2: Write the failing test** (`salifecycle_payload_test.go`)

```go
package server

import (
	"context"
	"testing"

	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

func TestSALifecyclePayload_stampsRequestMeta(t *testing.T) {
	ctx := service.WithRequestMeta(context.Background(), "198.51.100.9", "key-42")
	ev := service.AuditEvent{
		Action:   "service_account.key_issued",
		ActorID:  "actor-1",
		Resource: "sa-1",
	}

	p := saLifecyclePayload(ctx, ev)

	if p.SourceIP != "198.51.100.9" {
		t.Errorf("SourceIP = %q, want 198.51.100.9", p.SourceIP)
	}
	if p.APIKeyID != "key-42" {
		t.Errorf("APIKeyID = %q, want key-42", p.APIKeyID)
	}
	if p.Action != "service_account.key_issued" || p.Resource != "sa-1" {
		t.Errorf("core fields not preserved: %+v", p)
	}
}

func TestSALifecyclePayload_bareContext_emptyMeta(t *testing.T) {
	p := saLifecyclePayload(context.Background(), service.AuditEvent{Action: "service_account.created"})
	if p.SourceIP != "" || p.APIKeyID != "" {
		t.Errorf("bare ctx should yield empty meta, got %+v", p)
	}
}
```

- [ ] **Step 3: Run to verify it fails**

Run: `cd services/auth && GOWORK=off go test ./internal/server/ -run TestSALifecyclePayload -v`
Expected: compile failure — `saLifecyclePayload` undefined.

- [ ] **Step 4: Add the pure builder + use it in `publishSALifecycle`** (`server.go`)

Add the builder near `publishSALifecycle`:

```go
// saLifecyclePayload builds the ServiceAccountLifecyclePayload for an SA
// lifecycle AuditEvent, stamping the request's source IP + authenticating
// API-key id read from ctx (see service.RequestMetaFromContext). Pure +
// broker-free so it is unit-testable.
func saLifecyclePayload(ctx context.Context, ev service.AuditEvent) events.ServiceAccountLifecyclePayload {
	sourceIP, apiKeyID := service.RequestMetaFromContext(ctx)
	return events.ServiceAccountLifecyclePayload{
		Action:   ev.Action,
		ActorID:  ev.ActorID,
		Resource: ev.Resource,
		Fields:   ev.Fields,
		SourceIP: sourceIP,
		APIKeyID: apiKeyID,
	}
}
```

Change the body of `publishSALifecycle` to use it — replace the inline struct literal:

```go
func (e rabbitMQAuditEmitter) publishSALifecycle(ctx context.Context, ev service.AuditEvent) error {
	payload, err := json.Marshal(saLifecyclePayload(ctx, ev))
	if err != nil {
		return fmt.Errorf("marshal SA lifecycle payload: %w", err)
	}
	envelope := events.Event{
		ID:         uuid.New().String(),
		Type:       events.RoutingServiceAccountLifecycle,
		TenantID:   ev.TenantID,
		OccurredAt: time.Now(),
		Version:    "1.0",
		Payload:    payload,
	}
	if err := e.pub.Publish(ctx, events.RoutingServiceAccountLifecycle, envelope); err != nil {
		return fmt.Errorf("publish SA lifecycle: %w", err)
	}
	return nil
}
```

- [ ] **Step 5: Run to verify it passes + both modules still build**

Run:
```
cd services/auth && GOWORK=off go test ./internal/server/ -run TestSALifecyclePayload -v && GOWORK=off go build ./...
cd ../audit && GOWORK=off go build ./...
```
Expected: PASS + both build (the `libs/rabbitmq/events` struct change must not break audit).

- [ ] **Step 6: Commit**

```bash
git add libs/rabbitmq/events/events.go services/auth/internal/server/server.go services/auth/internal/server/salifecycle_payload_test.go
git commit -m "$(cat <<'EOF'
feat(events,auth): stamp source_ip + api_key_id onto SA lifecycle events

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Capture at the SA HTTP handlers (auth)

**Files:**
- Modify: `services/auth/internal/handler/http.go` (`synthClaimsFromAPIKey` ~line 993; add `auditCtx` helper; `createSAAPIKey` call site ~line 802)
- Modify: `services/auth/internal/handler/http_service_accounts.go` (call sites at 275, 411, 429, 494, 700, 771)
- Test: `services/auth/internal/handler/audit_ctx_test.go` (**new**)

**Context:** `requireAuth(r)` returns `*service.Claims`. Each SA mutation handler already calls it (directly or to derive `callerID`). We add a helper that builds the enriched ctx from `(r, claims)` and pass it to the emitting service call instead of `r.Context()`.

- [ ] **Step 1: Make `synthClaimsFromAPIKey` carry the key id** (`http.go`)

Change the signature + call site so the parsed `keyID` reaches the claims:

At the call site in `requireAuth` (~line 953) `return synthClaimsFromAPIKey(vk), nil` → `return synthClaimsFromAPIKey(vk, keyID), nil` (the `keyID` from `parseAPIKeyBearer` at line 940 is in scope).

Update the function (~line 993):

```go
func synthClaimsFromAPIKey(vk *service.ValidatedKey, keyID uuid.UUID) *service.Claims {
	c := &service.Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: vk.UserID.String(),
		},
		TenantID: vk.TenantID.String(),
		Access:   vk.Access,
		// ... KEEP every other field exactly as it currently is ...
	}
	c.KeyID = keyID
	return c
}
```

> Read the current `synthClaimsFromAPIKey` body first and preserve all existing field assignments (PrincipalKind, Roles, etc.) — only add the `keyID` param and the `c.KeyID = keyID` line. There is exactly one caller (the `requireAuth` return at ~line 953).

- [ ] **Step 2: Write the failing test** (`audit_ctx_test.go`)

```go
package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

func TestAuditCtx_apiKeyRequest_capturesIPAndKeyID(t *testing.T) {
	h := &HTTPHandler{} // remoteIP falls back to the TCP peer with no trusted proxies configured
	r := httptest.NewRequest("POST", "/service-accounts", nil)
	r.RemoteAddr = "203.0.113.5:44321"
	kid := uuid.New()

	ctx := h.auditCtx(r, &service.Claims{KeyID: kid})

	ip, keyID := service.RequestMetaFromContext(ctx)
	if ip != "203.0.113.5" {
		t.Errorf("source ip = %q, want 203.0.113.5", ip)
	}
	if keyID != kid.String() {
		t.Errorf("api key id = %q, want %s", keyID, kid)
	}
}

func TestAuditCtx_jwtRequest_blankKeyID(t *testing.T) {
	h := &HTTPHandler{}
	r := httptest.NewRequest("POST", "/service-accounts", nil)
	r.RemoteAddr = "203.0.113.5:44321"

	ctx := h.auditCtx(r, &service.Claims{}) // KeyID == uuid.Nil

	_, keyID := service.RequestMetaFromContext(ctx)
	if keyID != "" {
		t.Errorf("JWT request should have blank api_key_id, got %q", keyID)
	}
}
```

> Verify `remoteIP(r)` returns the bare host `203.0.113.5` for a request with `RemoteAddr` set and no trusted proxies. If the current `remoteIP` needs handler config (a `TrustedProxies` field) to be non-nil, construct `h` with whatever zero-value config makes `remoteIP` return the TCP peer (that is its documented default when `TRUSTED_PROXY_CIDRS` is empty — SEC-009). Adjust the expected host only if `remoteIP` strips the port differently.

- [ ] **Step 3: Run to verify it fails**

Run: `cd services/auth && GOWORK=off go test ./internal/handler/ -run TestAuditCtx -v`
Expected: compile failure — `auditCtx` undefined.

- [ ] **Step 4: Add the `auditCtx` helper** (`http.go`, near `requireAuth`)

```go
// auditCtx enriches the request context with the actor metadata the audit
// emitter stamps onto lifecycle events: the trusted-proxy-resolved client IP
// and the authenticating API-key id (blank for JWT/browser callers). Handlers
// that trigger an audited SA mutation pass auditCtx(r, claims) to the service
// call instead of r.Context().
func (h *HTTPHandler) auditCtx(r *http.Request, claims *service.Claims) context.Context {
	keyID := ""
	if claims != nil && claims.KeyID != uuid.Nil {
		keyID = claims.KeyID.String()
	}
	return service.WithRequestMeta(r.Context(), remoteIP(r), keyID)
}
```

Ensure `http.go` imports `context` and `github.com/google/uuid` (it already uses both).

- [ ] **Step 5: Thread `auditCtx` into the SA mutation call sites**

For each of these emitting calls, replace the `r.Context()` argument with `h.auditCtx(r, claims)`, where `claims` is the `*service.Claims` that handler already obtained from `requireAuth` (if a handler only kept `callerID`, capture the full `claims` return value too — do not re-call `requireAuth`):

- `http.go:802` — `h.saService.IssueKey(r.Context(), …)` → `h.saService.IssueKey(h.auditCtx(r, claims), …)`
- `http_service_accounts.go:275` — `h.saService.Create(r.Context(), …)`
- `http_service_accounts.go:411` — `h.saService.SetDisabled(r.Context(), …)`
- `http_service_accounts.go:429` — `h.saService.Update(r.Context(), …)`
- `http_service_accounts.go:494` — `h.saService.Delete(r.Context(), …)`
- `http_service_accounts.go:700` — `h.saService.IssueKey(r.Context(), …)`
- `http_service_accounts.go:771` — `h.saService.RevokeKey(r.Context(), …)`

> Read each handler; confirm the `claims` variable name (some may name it `caller` or `claims`). If a scopes-update mutation exists as a distinct handler/method not in this list (e.g. a dedicated `UpdateScopes`), thread `auditCtx` there too — the rule is: every handler whose service call ends in `s.audit.Emit` gets `auditCtx`. Read-only calls (`List`, `Get`, `ListKeys`, `CountKeysAffectedByScopeShrink`) must stay on `r.Context()`.

- [ ] **Step 6: Run to verify handler tests pass + build**

Run: `cd services/auth && GOWORK=off go test ./internal/handler/ -run TestAuditCtx -v && GOWORK=off go test ./internal/handler/ && GOWORK=off go build ./...`
Expected: PASS, whole handler package green, build OK.

- [ ] **Step 7: Commit**

```bash
git add services/auth/internal/handler/http.go services/auth/internal/handler/http_service_accounts.go services/auth/internal/handler/audit_ctx_test.go
git commit -m "$(cat <<'EOF'
feat(auth): capture source_ip + api_key_id at SA lifecycle handlers

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Audit consumer stores source_ip in actor_ip (audit)

**Files:**
- Modify: `services/audit/internal/eventconsumer/consumer.go` (`RoutingServiceAccountLifecycle` case, ~line 663)
- Test: `services/audit/internal/eventconsumer/consumer_test.go` (extend)

- [ ] **Step 1: Write the failing test**

Add to `consumer_test.go` (mirror an existing `mapEvent` test — find one that feeds a `ServiceAccountLifecyclePayload` and asserts the resulting `AuditEvent`; copy its harness):

```go
func TestMapEvent_saLifecycle_populatesActorIP(t *testing.T) {
	payload, _ := json.Marshal(events.ServiceAccountLifecyclePayload{
		Action:   "service_account.key_issued",
		ActorID:  "actor-1",
		Resource: "sa-1",
		SourceIP: "203.0.113.9",
		APIKeyID: "key-77",
	})
	ev := &events.Event{
		ID:         uuid.New().String(),
		Type:       events.RoutingServiceAccountLifecycle,
		TenantID:   uuid.New().String(),
		OccurredAt: time.Now(),
		Payload:    payload,
	}

	ae := mapEvent(ev) // adjust to the real mapEvent signature/receiver in this file

	if ae == nil {
		t.Fatal("mapEvent returned nil for SA lifecycle event")
	}
	if ae.ActorIP != "203.0.113.9" {
		t.Errorf("ActorIP = %q, want 203.0.113.9", ae.ActorIP)
	}
	if ae.Action != "service_account.key_issued" {
		t.Errorf("Action = %q, want service_account.key_issued", ae.Action)
	}
}
```

> Read `consumer.go` to get the exact `mapEvent` signature (it may be a method `c.mapEvent(...)` and may take `(tenantID, event)` separately) and an existing SA-case test to copy the invocation + imports precisely. The assertion that matters is `ae.ActorIP == payload.SourceIP`.

- [ ] **Step 2: Run to verify it fails**

Run: `cd services/audit && GOWORK=off go test ./internal/eventconsumer/ -run TestMapEvent_saLifecycle_populatesActorIP -v`
Expected: FAIL — `ActorIP` is empty (not yet mapped).

- [ ] **Step 3: Implement — set `ActorIP` in the SA case** (`consumer.go`, ~line 663)

In the `case events.RoutingServiceAccountLifecycle:` block, after unmarshalling `var p events.ServiceAccountLifecyclePayload`, set `ActorIP` on the returned `AuditEvent`:

```go
	case events.RoutingServiceAccountLifecycle:
		var p events.ServiceAccountLifecyclePayload
		_ = json.Unmarshal(event.Payload, &p)
		// ... existing actor/action/resource/metadata construction ...
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    p.ActorID,
			ActorType:  actorType, // whatever the existing code uses
			ActorIP:    p.SourceIP, // NEW — client IP → audit_events.actor_ip
			Action:     p.Action,
			Resource:   p.Resource,
			Outcome:    "success",
			Metadata:   meta, // unchanged: {"event_id":…, "raw": <payload>} — carries api_key_id
			OccurredAt: now,
		}
```

> Read the actual SA case first and add only the `ActorIP: p.SourceIP` field to the existing returned `AuditEvent` literal — preserve every other field exactly. `api_key_id` needs no consumer change: it rides inside the `raw` payload already stored in `Metadata`, and Task 5 extracts it in the read path.

- [ ] **Step 4: Run to verify it passes + package green**

Run: `cd services/audit && GOWORK=off go test ./internal/eventconsumer/ -v 2>&1 | tail -5`
Expected: new test PASS, package green.

- [ ] **Step 5: Commit**

```bash
git add services/audit/internal/eventconsumer/consumer.go services/audit/internal/eventconsumer/consumer_test.go
git commit -m "$(cat <<'EOF'
feat(audit): map SA lifecycle source_ip into audit_events.actor_ip

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Surface source_ip + api_key_id in notification metadata (audit)

**Files:**
- Modify: `services/audit/internal/handler/notifications.go` (`rawNotificationPayload` ~line 226; `notificationMetadataMap` ~line 567; its caller in `notificationFromRow` ~line 316)
- Test: `services/audit/internal/handler/notifications_outcome_test.go` (extend — this file was added by PR #377)

- [ ] **Step 1: Write the failing test** (extend `notifications_outcome_test.go`)

```go
func TestNotificationFromRow_surfacesSourceIPAndAPIKeyID(t *testing.T) {
	row := &repository.NotificationRow{
		ID:         uuid.New(),
		ActorID:    "actor-1",
		ActorType:  "user",
		Action:     "service_account.key_issued",
		Outcome:    "success",
		ActorIP:    "203.0.113.9",
		Metadata:   json.RawMessage(`{"raw":{"api_key_id":"key-77"}}`),
		OccurredAt: time.Now(),
	}

	ev := notificationFromRow(row, nil)

	if got := ev.GetMetadata()["source_ip"]; got != "203.0.113.9" {
		t.Errorf("metadata[source_ip] = %q, want 203.0.113.9", got)
	}
	if got := ev.GetMetadata()["api_key_id"]; got != "key-77" {
		t.Errorf("metadata[api_key_id] = %q, want key-77", got)
	}
}
```

> Confirm `repository.NotificationRow` has an `ActorIP` field. It mirrors `AuditEvent`; if the `NotificationRow` struct (repository.go ~line 372) does **not** yet carry `ActorIP`, add it to the struct AND to the `GetNotifications` SQL SELECT + row scan so `actor_ip` is read from the column. (Check the `GetNotifications` query in `services/audit/internal/repository/repository.go` — if it selects an explicit column list, add `actor_ip` and scan it into the new field. This is required for `row.ActorIP` to be non-empty at read time.)

- [ ] **Step 2: Run to verify it fails**

Run: `cd services/audit && GOWORK=off go test ./internal/handler/ -run TestNotificationFromRow_surfacesSourceIPAndAPIKeyID -v`
Expected: FAIL — keys absent.

- [ ] **Step 3: Add `APIKeyID` to `rawNotificationPayload`** (`notifications.go` ~line 226)

```go
	// APIKeyID is the id of the API key that authenticated the request that
	// produced this event (auth SA lifecycle events, FUT-088 follow-up). Empty
	// for JWT/browser-driven mutations. Surfaced as metadata["api_key_id"].
	APIKeyID string `json:"api_key_id"`
```

- [ ] **Step 4: Extend `notificationMetadataMap` + its caller**

Change the signature to also take the row's actor IP, and set both keys:

```go
func notificationMetadataMap(p *rawNotificationPayload, outcome, sourceIP string) map[string]string {
	m := map[string]string{}
	if outcome != "" {
		m["outcome"] = outcome
	}
	// source_ip is the audit row's actor_ip column (auth-published access
	// events); api_key_id rides in the raw payload. Both power the principal
	// activity feed (auth ActivityService reads these metadata keys).
	if sourceIP != "" {
		m["source_ip"] = sourceIP
	}
	if p.APIKeyID != "" {
		m["api_key_id"] = p.APIKeyID
	}
	if p.RepositoryName != "" {
		m["repo"] = p.RepositoryName
	}
	// ... rest unchanged ...
}
```

Update the caller in `notificationFromRow` (~line 316):

```go
		Metadata:         notificationMetadataMap(&p, row.Outcome, row.ActorIP),
```

- [ ] **Step 5: Run to verify it passes + package green + no regression**

Run: `cd services/audit && GOWORK=off go test ./internal/handler/ 2>&1 | tail -5`
Expected: new test + `TestNotificationFromRow_populatesOutcomeMetadata` (from #377) + full package PASS.

- [ ] **Step 6: Commit**

```bash
git add services/audit/internal/handler/notifications.go services/audit/internal/handler/notifications_outcome_test.go services/audit/internal/repository/repository.go
git commit -m "$(cat <<'EOF'
feat(audit): surface source_ip + api_key_id in notification metadata

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Gates, docs, tracker, live-verify

**Files:**
- Modify: `docs/SERVICES.md` (auth activity-feed / FE-API-048 section), `status.md`

- [ ] **Step 1: Per-service gates**

Run:
```
cd services/auth  && GOWORK=off go build ./... && GOWORK=off go test ./... && GOWORK=off go vet ./...
cd ../audit && GOWORK=off go build ./... && GOWORK=off go test ./... && GOWORK=off go vet ./...
```
Then `golangci-lint run ./...` in each (if installed). Expected: all green (a pre-existing unrelated `copylocks` in `services/management` does not apply here). Fix anything in the touched files.

- [ ] **Step 2: Live-verify on the compose stack**

Rebuild + restart auth and audit:
```
cd infra/docker-compose && docker compose build registry-auth registry-audit && docker compose up -d registry-auth registry-audit
```
Then drive both auth modes and inspect the feed:
- **API-key path:** using an `key.<id>.<secret>` API key with SA-admin, `POST /api/v1/service-accounts` (create an SA) so a `service_account.created` event fires while authenticated by that key.
- **JWT path:** log in as admin (browser/JWT), create/modify an SA.
- Query `GET :8080/api/v1/access/activity?principal_user_id=<admin sub>&since=<recent>` and confirm: the API-key-driven rows carry both `source_ip` and `api_key_id`; the JWT-driven rows carry `source_ip` and a **blank** `api_key_id`.

Record the observed JSON in the PR description.

- [ ] **Step 3: Docs + tracker**

- `docs/SERVICES.md`: in the registry-auth activity-feed / FE-API-048 notes, record that `source_ip` + `api_key_id` are now populated for the `service_account.*` events, that `source_ip` uses `audit_events.actor_ip`, and that core push/pull enrichment is deferred (Phase 2).
- `status.md`: prepend a row for this change; note it closes the "out of scope" caveat from PR #377.

- [ ] **Step 4: Commit**

```bash
git add docs/SERVICES.md status.md
git commit -m "$(cat <<'EOF'
docs: activity feed now populates source_ip + api_key_id (auth access events)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-Review notes (reconciled)

- **Spec coverage:** capture via ctx (Tasks 1+3), emitter stamping (Task 2), payload fields (Task 2), source_ip → `actor_ip` column (Task 4), api_key_id in metadata (Task 5), surfacing in `notificationMetadataMap` (Task 5), reader unchanged (already reads the keys), testing at each layer + live-verify (Task 6), docs/tracker (Task 6). All spec sections mapped.
- **api_key_id semantics** = authenticating key (from `Claims.KeyID`, populated only on the API-key auth path) — matches the approved decision; blank for JWT is expected, not a bug.
- **Type consistency:** `WithRequestMeta`/`RequestMetaFromContext` (Task 1) used verbatim in Tasks 2+3; `ServiceAccountLifecyclePayload.SourceIP/APIKeyID` (Task 2) consumed in Tasks 4+5; `notificationMetadataMap(p, outcome, sourceIP)` signature (Task 5) matches its single caller update.
- **Verify-in-place points flagged inline:** exact `remoteIP` port-stripping behavior; `synthClaimsFromAPIKey` current body preservation; the precise `mapEvent` signature + an SA-case test to copy; whether `NotificationRow` already carries `ActorIP` (and if not, add it to the struct + `GetNotifications` SELECT/scan); the exact `claims` variable name in each SA handler. Each has a concrete instruction.
- **Dependency:** stacks on PR #377 (shared `notificationMetadataMap` + `notifications_outcome_test.go`). Rebase onto main once #377 merges before opening this PR.

# Agent Definitions

> This file defines the specialised agents available for AI-assisted development of the registry platform.
> Invoke each agent by role when its domain is relevant. Each agent has a defined scope, inputs, outputs, and rules.

---

## Agent Index

| Agent | Role | Primary Trigger |
|---|---|---|
| [QA Agent](#qa-agent) | Test coverage, quality gates, conformance | Before any service is marked `DONE` |
| [Security Agent](#security-agent) | Security review, threat modelling, hardening audit | Before any PR merges, on new endpoints/auth changes |
| [Project Management Agent](#project-management-agent) | Status tracking, sprint planning, blockers | Sprint boundaries, new service kickoff |
| [Code Review Agent](#code-review-agent) | Code quality, conventions, correctness | Every PR |

---

## QA Agent

**Role:** Ensure every service meets the platform's testing requirements before it ships. Catch missing coverage, absent integration tests, and conformance gaps.

**Invoke when:**
- A service is moving from `IN PROGRESS` → `IN REVIEW`
- A new endpoint or gRPC method is added
- The OCI conformance suite needs to be run against `registry-core`
- Integration test infrastructure changes

**Inputs expected:**
- Service name
- Changed files or PR diff
- Current test coverage report (if available)

**Responsibilities:**

1. **Unit test audit**
   - Verify 80% coverage minimum per service (CI enforced)
   - Check test naming follows `Test<FunctionName>_<scenario>_<expectedOutcome>`
   - Confirm table-driven tests are used for all validation logic
   - Confirm no real network calls in unit tests (interfaces + mocks only)
   - Verify mocks generated with `mockery`, not hand-rolled

2. **Integration test audit**
   - Confirm integration tests exist in `internal/testutil/integration/`
   - Verify `testcontainers-go` is used (real PostgreSQL, Redis, RabbitMQ, MinIO per suite)
   - Confirm tests tagged `//go:build integration`
   - Verify `make test-integration` target exists and passes

3. **OCI conformance (registry-core only)**
   - Run OCI Distribution Spec conformance suite
   - All required endpoints must pass
   - `make test-conformance` must exit 0
   - Confirm `make test-conformance` is wired into CI on PRs to `main`

4. **Race condition check**
   - Confirm `go test -race ./...` passes (CI enforced)
   - Flag any `sync` primitives not covered by race tests

5. **Edge case checklist per service type**
   - Storage service: zero-byte blobs, exact quota boundary, interrupted multipart upload
   - Auth service: expired token, revoked token, locked account, wrong audience
   - Core service: digest mismatch on complete upload, chunked upload offset gap, unknown media type
   - GC: dry-run vs live, blob younger than `GC_BLOB_MIN_AGE`, concurrent run attempted

**Output format:**
```
QA Review — <service> — <date>
PASS / FAIL

Unit tests:     PASS | FAIL | NOT RUN — <note>
Integration:    PASS | FAIL | NOT RUN — <note>
Conformance:    PASS | FAIL | N/A     — <note>
Race check:     PASS | FAIL | NOT RUN — <note>

Issues:
- [BLOCKER] <description>
- [WARNING] <description>

Cleared for: IN REVIEW | NOT CLEARED
```

---

## Security Agent

**Role:** Identify security issues before code ships. Perform threat modelling on new features. Verify hardening checklist compliance. Track issues in `security.md`.

**Invoke when:**
- Any authentication or authorisation logic changes
- A new HTTP or gRPC endpoint is added
- A new external integration is added (storage backend, scanner plugin, upstream registry)
- A service is moving from `IN REVIEW` → `DONE`
- A new dependency is added

**Inputs expected:**
- Service name + changed files or PR diff
- New endpoints or auth flows (described in plain language if not obvious from diff)

**Responsibilities:**

1. **Hardening checklist verification** (against `§17` in CLAUDE.md)
   - No `unsafe`, no `exec.Command` with user input, no `os.Getenv` in handlers
   - All file paths sanitised with `filepath.Clean` + prefix check
   - HTTP clients have timeouts set, no `http.DefaultClient`
   - `crypto/rand` used for all security-sensitive randomness
   - Request body size limits on all HTTP servers
   - CORS explicitly configured (no `*`)
   - `X-Content-Type-Options: nosniff` on all responses

2. **Input validation review**
   - Every user-supplied string validated against the allowlist regexes in `§7`
   - No unvalidated input reaches SQL, shell, or storage key construction
   - Parameterised queries only — no `fmt.Sprintf` in SQL

3. **Auth and token flow review**
   - JWT validation present on every gRPC server (or explicit documented exemption)
   - Tenant ID cross-check (`X-Tenant-ID` vs JWT `tenant_id`) enforced
   - API key stored as argon2id hash, never plaintext
   - `jti` stored in Redis with TTL derived from `time.Until(claims.ExpiresAt.Time)`

4. **Secrets hygiene**
   - No secrets in committed files (`.env` files, hardcoded strings)
   - All secrets loaded at startup into typed config struct
   - Service fails to start if required secret env var is empty
   - No secrets logged at any level

5. **Network boundary review**
   - SSRF checks on any service that makes outbound HTTP calls (proxy, webhook)
   - Webhook destination URL validated against private IP blocklist
   - No presigned URLs exposed to clients

6. **Dependency check**
   - `govulncheck` output reviewed — no CRITICAL or HIGH unaddressed
   - No GPL/AGPL dependencies without documented approval
   - New indirect deps have a comment in `go.mod` explaining why

7. **Threat model for new features**
   - For any new user-facing feature: identify trust boundaries crossed, data flows, and attacker capabilities
   - Document findings as new entries in `security.md` if actionable

**Output format:**
```
Security Review — <service> — <date>
PASS / FAIL / CONDITIONAL

Hardening checklist:  PASS | ISSUES FOUND
Input validation:     PASS | ISSUES FOUND
Auth/token flow:      PASS | ISSUES FOUND | N/A
Secrets hygiene:      PASS | ISSUES FOUND
Network boundaries:   PASS | ISSUES FOUND | N/A
Dependencies:         PASS | ISSUES FOUND

New security.md entries: <list of SEC-XXX IDs added>

Blockers (must fix before merge):
- <description>

Warnings (fix within sprint):
- <description>

Cleared for merge: YES | NO
```

---

## Project Management Agent

**Role:** Keep `status.md` accurate. Surface blockers early. Identify build order dependencies. Flag when decisions are stale or missing.

**Invoke when:**
- Starting a new sprint or planning session
- A new service is about to be kicked off
- A service status changes
- An open decision has been unresolved for more than 2 weeks
- A dependency between services is unclear

**Inputs expected:**
- What changed or what you want to plan
- Any new blockers or resolved decisions

**Responsibilities:**

1. **Status tracking**
   - Update service status in `status.md` when work progresses
   - Ensure `Owner` and `Notes` columns are current
   - Flag any service marked `IN PROGRESS` with no recent activity

2. **Build order enforcement**
   - Recommended build order: `proto/` → `libs/` → `services/auth` → `services/metadata` → `services/storage` → `services/core` → remaining services in parallel
   - Warn if a service is started before its dependencies are `DONE`
   - Track which services share gRPC contracts that must be stable before downstream work begins

3. **Open decisions**
   - Review open decisions table in `status.md`
   - Flag any decision blocking more than one service
   - Prompt for resolution if a decision has been open > 2 weeks

4. **Sprint management**
   - Update the Current Sprint table in `status.md`
   - Ensure each task maps to a specific service
   - At sprint end: summarise what shipped, what carried over, and why

5. **Cross-service coordination**
   - Identify when a change in one service requires a corresponding change in another
   - Flag proto breaking changes (require major version bump per `§15`)
   - Track `libs/` changes that affect multiple services

**Output format:**
```
PM Review — <date>

Status updates applied: <list>
Decisions resolved: <list>
New blockers identified: <list>
Build order warnings: <list>
Open decisions older than 14 days: <list>

status.md: UPDATED | NO CHANGES NEEDED
```

---

## Code Review Agent

**Role:** Review code for correctness, adherence to platform conventions, and quality. Not a security review (delegate to Security Agent for that).

**Invoke when:**
- A service is moving to `IN REVIEW`
- A shared library (`libs/`) or proto definition (`proto/`) changes
- A database migration is added
- A new gRPC service or method is added

**Inputs expected:**
- PR diff or list of changed files
- Service name
- Brief description of what the change does

**Responsibilities:**

1. **Architecture conformance**
   - Service follows the per-repo layout defined in `§2`
   - No business logic in `cmd/server/main.go` — entrypoint only
   - No raw SQL outside `internal/repository/`
   - No direct PostgreSQL access from services other than `registry-metadata`, `registry-auth`, `registry-tenant`
   - No circular imports between packages

2. **Go conventions**
   - `pgx/v5` with `pgxpool`, no ORM
   - All queries parameterised
   - `defer tx.Rollback(ctx)` pattern used in all transactions
   - Request context propagated through all calls — no `context.Background()` in handlers
   - gRPC deadlines set on all outgoing calls: `context.WithTimeout(ctx, 5*time.Second)`
   - `grpc.WithBlock()` with timeout on all client connections

3. **Configuration**
   - Config loaded via Viper into a typed struct in `internal/config/`
   - Service fails to start on missing required config (`config.Validate()` called in `main.go`)
   - No `os.Getenv` calls outside the config loader
   - `.env.example` updated with any new env vars

4. **Error handling**
   - gRPC errors use `google.rpc.Status` with appropriate `codes.*`
   - `codes.ResourceExhausted` for pool exhaustion (not `codes.Internal`)
   - No errors silently swallowed — log or return, not both without reason
   - Panic recovery interceptor present (via `libs/middleware/grpc`)

5. **Proto conventions** (if proto files changed)
   - Package naming: `registry.<service>.v1`
   - `go_package` option set correctly
   - All fields `snake_case`
   - No field numbers modified on existing messages
   - New fields added only (never remove or renumber)
   - Timestamps use `google.protobuf.Timestamp`
   - UUIDs as `string`

6. **Database migrations** (if migrations added)
   - Named `YYYYMMDDHHMMSS_<description>.sql`
   - Down migration present and correct
   - No column drops (add + migrate in separate step)
   - New indexes added for any new foreign keys or filter columns
   - `sslmode=require` still enforced in connection string

7. **Observability**
   - OTEL bootstrap called in `main.go` with deferred shutdown
   - Structured logging uses `log/slog`, JSON format
   - `trace_id`, `span_id`, `tenant_id` present in log entries where available
   - New operations add spans — no silent gaps in traces
   - Prometheus metrics endpoint (`GET /metrics`) present on internal port

8. **Test quality**
   - New code has corresponding tests
   - Test names follow `Test<FunctionName>_<scenario>_<expectedOutcome>`
   - No test helper logic in production code paths
   - No `time.Sleep` in tests — use channels or `testcontainers` readiness checks

**Output format:**
```
Code Review — <service> — <date>
APPROVE / REQUEST CHANGES / BLOCKING

Architecture:     PASS | ISSUES
Go conventions:   PASS | ISSUES
Configuration:    PASS | ISSUES
Error handling:   PASS | ISSUES
Proto:            PASS | ISSUES | N/A
Migrations:       PASS | ISSUES | N/A
Observability:    PASS | ISSUES
Tests:            PASS | ISSUES

Blocking issues (must fix before merge):
- <description> — <file>:<line>

Non-blocking suggestions:
- <description> — <file>:<line>

Decision: APPROVE | REQUEST CHANGES
```

---

## How to Use These Agents

When invoking an agent, state:
1. Which agent you want
2. The service or change in scope
3. Any relevant context (PR link, changed files, what the change does)

Example: *"Run the Security Agent on registry-auth — I just added the API key creation endpoint."*

Agents can be run in sequence (PM → Code Review → Security → QA) for a full pre-merge pipeline, or individually for spot checks.

# Security Issues

> **Open as of 2026-07-03: 13 SEC findings — all LOW/INFO, no open HIGH or MEDIUM — plus PENTEST-030 (OPEN) and PENTEST-033 (PARTIAL).** LOW: SEC-037, SEC-048, SEC-053, SEC-054, SEC-055, SEC-056, SEC-071. INFO: SEC-049, SEC-063, SEC-072, SEC-073, SEC-074, SEC-075. _Supersedes the "15 OPEN as of 2026-07-02" snapshot lower down:_ SEC-057..062 resolved (#244), SEC-064/065/067/068/069/070 resolved (#226/#228/#243), SEC-066 closed WON'T FIX (#246); SEC-071..075 logged since (RED-FU-015 / PR #250 reviews). See each entry below for triage.
>
> Last updated: 2026-07-03 — Reviewed PR #250 (`chore/ci-consolidate-govulncheck`) — infra-only (CI workflows + docs + trackers; no service/lib/proto code). **PR PASSES — NO SECURITY REGRESSION, no blockers.** **Change B (removal of `scripts/lint-user-queries.sh` + its `ci-auth.yml` step, REM-015):** VERIFIED SAFE. The script was a repository-layer SQL-annotation lint (every `FROM users` SELECT in `services/auth/internal/repository/` must filter by `kind` or carry an `-- allow-any-kind`/`// allow-any-kind` annotation) and was already `continue-on-error: true` — it NEVER gated merges. Crucially it never enforced *caller-side* routing, only annotation presence. The real runtime kind-guard is the repository `…Human…` helper contract + the empty `password_hash` on SA shadow rows, none of which this PR touches: SSO email match routes through `GetHumanByEmail` (kind='human' guard) at `services/auth/internal/service/sso.go:412,498`; `GetHumanByEmail`/`GetHumanByID` reject SA rows, pinned by integration tests in `services/auth/internal/repository/user_human_test.go`; SA shadow rows are created with `password_hash=''` (`services/auth/internal/repository/service_account.go:144`) so `argon2.Verify` rejects every password. One genuine (pre-existing, non-exploitable) defense-in-depth gap surfaced and logged as **SEC-075 (INFO)** — the username/password login path (`AuthenticateUser`, `service/auth.go:1080`) uses the kind-agnostic `GetByUsername`, and the `GetHumanByUsername` helper referenced in docstrings + the deleted lint's guidance does not exist; the sole barrier there is the empty password_hash, not the kind guard. **Change A (govulncheck consolidation to nightly `ci-security.yml`):** VERIFIED — removes NO blocking gate. The 13 per-service `security:` jobs were doubly non-blocking (`continue-on-error: true` AND a trailing `|| true`), so they produced ~26 permanently-green checks with zero merge signal; the new scheduled matrix job drops the `|| true` so its exit code now tracks govulncheck for real (REM-016 flip-to-blocking is a one-line `continue-on-error` deletion). No merge-time signal is lost because the pre-state provided none. No CRITICAL/HIGH.
>
> Last updated: 2026-07-03 — SEC-071..SEC-074 logged (1 LOW, 3 INFO) from the pre-PR review of `feat/red-fu-015-kek-rotation` (PR #249, RED-FU-015 KEK rotation tool). **PR PASSES — no CRITICAL/HIGH, no blockers.** The crypto core (`libs/crypto/rekey`) is sound: `Rekey` never returns or logs plaintext; error paths carry only table/column/PK + GCM "authentication failed" (no key/secret material); keys are validated 32-byte + hex before use and come from env, never flags. Per-table transactions are genuinely all-or-nothing (`defer tx.Rollback` + explicit `Commit` only after all cells re-encrypt) — a tampered/corrupt cell rolls the whole table back with the offending PK. SQL-injection VERIFIED SAFE: table/column/PK identifiers are interpolated only from compile-time-constant `TableSpec`s declared in each service's `internal/rotatekek/`, never from user input; all row values + version + PK are bound as `$N` parameters; `tableExists` even passes the table name as a bound value to `to_regclass($1)`. Column-coverage VERIFIED COMPLETE across all four services — auth `oauth_client_secret_enc` (current `global_sso_config` + legacy `auth_providers`, Optional), proxy `password_enc`, webhook `secret_enc` (hex-TEXT), audit `hmac_secret`+`bearer_token`; all use the same `libs/crypto/aes` codec Rekey re-encrypts with, and `saml_metadata_xml` is public IdP metadata (plain BYTEA cast, not KEK-encrypted) so its exclusion is correct. The subcommand dispatches before `config.Load` and starts no server/network endpoint — it does not weaken the running server's posture. Findings are all minor: SEC-071 (LOW, verify path holds needless `FOR UPDATE` locks), SEC-072 (INFO, `--generate` prints KEK to stdout), SEC-073 (INFO, no OLD==NEW key guard → silent no-op rotation), SEC-074 (INFO, plaintext buffer not zeroed — consistent with existing aes posture, not a regression). All accepted as should-fix follow-ups.
>
> Last updated: 2026-07-02 — **SEC-057..SEC-062 RESOLVED** on `fix/sec-057-062-oidc-jwks-hardening` (the FUT-001 OIDC/JWKS batch). SEC-057 (HIGH) core bypass was already closed in-code (issuerAllowed boundary check + tests); added the scheme-drop guard and **withdrew remediation #3 as incorrect** — trailing slashes on the compose issuer defaults would reject every legitimate slash-less `iss` (GitHub Actions et al.), so the in-code boundary check is the sole correct fix. SEC-058 (JWKS SSRF) closed via same-origin `jwks_uri` pin + no-redirect client; SEC-059 (OOM) via 1 MiB `io.LimitReader`; SEC-060 (TTL) via [60,86400] bound on Create+Update; SEC-061 via sha256-hashed rate-limit key; SEC-062 via explicit transport timeouts. All with regression tests; no new lint vs main. SEC-063 (INFO, https issuer enforcement) was already fixed separately in `validateOnCreate`. See each entry below for detail.
>
> Last updated: 2026-07-01 — SEC-068..SEC-070 logged (1 HIGH, 1 MEDIUM, 1 LOW) from the pre-PR review of `feat/fut-004-access-review` (already merged as PR #227 at review time — findings apply to `main`, remediation belongs on a follow-up branch). **HIGH: SEC-068** — `POST /api/v1/access/review/snooze` admin path skips tenant-scoping entirely; a workspace admin of tenant A can snooze any key id (including tenant B's) by passing `body.KeyID`, because `services/auth`'s `SnoozeAPIKeyReview` derives `tenant_id` from the row via `GetTenantIDForKey` with no cross-check against the caller. The audit event is stamped with the target key's tenant_id but the actor from tenant A — corrupts tenant B's audit trail and defers their review nudge. MEDIUM: SEC-069 (`SnoozeAPIKeyReviewRequest` gRPC surface has no `tenant_id` field AND trusts caller-supplied `actor_id` — same class as SEC-066 but a step worse because tenant scoping is impossible at the gRPC layer). LOW: SEC-070 (audit consumer swallows `json.Unmarshal` errors for `AccessReviewDuePayload` + `AccessReviewSnoozedPayload` — malformed payload silently writes an audit row with empty ActorID/Resource; consistent with pre-existing pattern, not a regression). PASSES: owner-vs-admin gate on LIST route correctly scopes to JWT tenant; SNOOZE non-admin path correctly uses `ListStaleKeys` pre-flight + 404 anti-enumeration; days bounds enforced at BE `service/access_review.go:257` AND BFF `access_review.go:160`; `actor_id` plumbed from JWT sub via `middleware.UserIDFromContext(r.Context())` at BFF `access_review.go:143`; both audit routing keys emit correctly (`RoutingAccessReviewDue` via dedicated `accessReviewPublisher`, `RoutingAccessReviewSnoozed` via explicit case in `rabbitMQAuditEmitter.Emit` — no `default:` swallow); worker `pg_try_advisory_lock` VERIFIED at `worker/access_review.go:174` + salt `"access-review:"` namespace-separated from FUT-003 idle-revoke; nudge-only invariant VERIFIED — worker only calls `PublishAccessReviewDue`, never `RevokeWithReason`; `review_snoozed_until IS NULL OR < now()` filter VERIFIED at `repository/apikey.go:391`.
>
> Last updated: 2026-07-01 — SEC-064..SEC-067 logged (1 HIGH, 1 MEDIUM, 2 LOW) from the pre-PR review of `feat/fut-003-token-policies`. **PR STATUS: BLOCKED on SEC-064** — `CreateAPIKey` policy check at `services/auth/internal/service/auth.go:657` guards with `expiresAt != nil`, so a caller who omits `expires_at` from the create request has its `max_ttl_days` cap silently bypassed and the resulting `api_keys` row has no expiry at all (immortal key). Grandfathering tests do not catch this because they always pass an explicit `expiresAt`. MEDIUM: SEC-066 (`PutTokenPolicy` gRPC surface trusts caller-supplied `tenant_id` without cross-check — safe in single mode via SingleTenantInjector, exposure limited to multi-mode + permissive mTLS CN allowlist). LOW: SEC-065 (FE `PoliciesPanel.validateSection` missing the BE's `idle_revoke_days >= 7` floor — no security impact, promised BE+FE parity gap); SEC-067 (`Upsert(all-nil)` rewrites `updated_by_user_id` on a no-op call — audit-trail credit-laundering vector). Grandfathering invariant VERIFIED — `TestCreateAPIKey_ExistingKeysGrandfathered` explicit and load-bearing. Idle-revoke advisory lock VERIFIED — `pg_try_advisory_lock` + `TestIdleRevoke_Tick_SkipsTenantsWithoutAdvisoryLock` prove multi-replica double-revoke prevention. `last_used_at` fail-OPEN VERIFIED — `TestLastUsedUpdater_RedisDown_FailOpen`. Actor id plumbing VERIFIED — BFF sources both `tenant_id` and `actor_id` from JWT (`access_token_policy.go:104-105`), test asserts `updated_by_user_id == "tenant-admin-user"` proving JWT sub is used.
>
> Last updated: 2026-07-01 — SEC-057..SEC-063 logged (1 HIGH, 3 MEDIUM, 2 LOW, 1 INFO) from the pre-PR review of `feat/fut-001-federated-workload-identity`. **PR STATUS: BLOCKED on SEC-057** — issuer allowlist uses `strings.HasPrefix` without a boundary check, so `https://token.actions.githubusercontent.com` in `OIDC_ALLOWED_ISSUERS` also matches attacker-registered `https://token.actions.githubusercontent.com.evil.example`, letting the attacker's IdP sign OIDC tokens that pass the trust gate. Compounded by the docker-compose default already shipping three vulnerable no-trailing-slash prefixes. MEDIUM: SEC-058 (JWKS SSRF — `jwks_uri` returned by discovery doc is followed without host/scheme constraint), SEC-059 (JWKS + discovery HTTP GET has no response body size cap → OOM), SEC-060 (`JWKSCacheTTLSeconds` has no min/max bound → DoS against IdP and against our JWKS cache). LOW: SEC-061 (workload rate-limit Redis key length uncapped from untrusted `sub`), SEC-062 (§13 hardening — JWKS `http.Client` only sets `Timeout`, not `TLSHandshakeTimeout`/`ResponseHeaderTimeout`). INFO: SEC-063 (issuer URL scheme not enforced HTTPS on backend — FE dialog checks but curl bypass allows `http://`). Two false-positives ruled out on review: fail-closed JWKS TTL semantics verified (line 92-107 fall through returns error, does NOT serve stale); BFF admin routes verified to take tenant_id from JWT via `middleware.TenantIDFromContext(r.Context())` at `access_oidc_trust.go:97/123/158/195`, body-field tenant_id ignored.
>
> Last updated: 2026-06-30 — SEC-055 + SEC-056 logged (both LOW) from the pre-PR review of `feat/fut-002-credential-helpers`. PR PASSES — no CRITICAL/HIGH. SEC-055: `frontend/src/lib/credential-snippets.ts` `sanitiseSAName` strips only `["\`$\\]` while the rendered snippets contain unquoted shell args (`--username ${safe}`, `--docker-username=${safe}`) and a YAML scalar (`username: ${safe}`); today this is safe because the server-side SA-name regex `^[a-z0-9]+([._-][a-z0-9]+)*$` admits no shell metacharacters, so the FE sanitizer is genuine defence-in-depth — but if the create-time regex is ever loosened, the FE silently regresses. SEC-056: `services/management/internal/handler/registry_info_test.go:11-13` docstring claims the endpoint is "unauthenticated by design (parallels handleDeploymentInfo)" but `handler.go:302` wraps it in `authMW`. Stale comment, not a code defect — but invites a future reviewer to "fix" the route by removing `authMW` to match the test claim. Both accepted as should-fix follow-ups.
>
> Last updated: 2026-06-30 — SEC-053 + SEC-054 logged (both LOW) from the pre-PR review of `feat/redesign-7.3-spec-lint` (REDESIGN-001 Phase 7.3). The spec-lint tool itself is benign (read-only, no `os.Create`/`exec.Command`/network; workflow uses `pull_request` not `pull_request_target`). Two false-negative paths flagged: (1) Rule #11 `// audit: skip` annotation has no allowlist so a bad-faith PR can silently exempt a sensitive event by adding the comment in the same diff that adds the event; (2) Rule #12 mTLS-validate gate matches the generic regex `cfg\.Validate\(\)` which any service-local `Validate()` satisfies, regardless of whether it actually runs the mTLS path check. Both accepted as should-fix follow-ups — they do not block the Phase 7.3 PR but should be tightened in a subsequent spec-lint hardening pass. Rule #4 (audit_chain_tip CREATE TABLE forbidden) was reviewed and is sufficient: any literal re-introduction is caught, and a rename would be visible in PR diff against §10 docs that name the column-derived tip.
>
> Last updated: 2026-06-30 — SEC-050 RESOLVED in `feat/redesign-6.12-audit-hash-chain` (REDESIGN-001 Phase 6.12). Pre-PR security-agent flagged the initial design as a HIGH BLOCKER: granting UPDATE on `audit_chain_tip` to `registry_audit_app` defeated the entire tamper-evidence posture — a compromised audit service could rewrite the tip to an earlier `row_hash` and INSERT a forged row chained off it without the linked-list verifier noticing. Redesign drops the separate tip table entirely and derives the tip from `audit_events.chain_seq` (BIGINT GENERATED ALWAYS AS IDENTITY) via `SELECT row_hash FROM audit_events WHERE tenant_id = $1 ORDER BY chain_seq DESC LIMIT 1`. `registry_audit_app` keeps INSERT-only on `audit_events` (FORCE RLS denies UPDATE/DELETE per Decision #15); the advisory lock alone provides per-tenant serialisation — no FOR UPDATE needed. SEC-051 (LOW, pre-migration rows are silently unverifiable) and SEC-052 (INFO, canonicaliseJSON NaN/Inf/>2^53 edge cases) tracked as follow-ups but accepted within Phase 6.12 scope.
>
> Last updated: 2026-06-30 — SEC-048 + SEC-049 logged + RESOLVED in `feat/redesign-6.5-jwks-rotation-multi-key`. SEC-048 (LOW, fallback DoS): hard cap `maxKeyRingSize = 16` in `loadKeyRingFromDir` + new `registry_auth_jwt_kid_fallback_total{reason}` counter so operators can alert on sustained-high fallback rates. SEC-049 (INFO, observability): boot-time `slog.Info "jwt key loaded"` with `(kid, pubkey_sha256, mtime)` per key so silent same-base overwrite collisions are visible; `pickDefaultSigningKID` switched from "lex greatest" to "most-recently-modified file" so naming conventions without timestamps still get the right default signer.
>
> Last updated: 2026-06-30 — SEC-046 / SEC-047 logged + RESOLVED in `feat/redesign-6.9-mtls-hot-reload` (REDESIGN-001 Phase 6.9). SEC-046 (INFO): added package-doc caveat clarifying that the cached-cert fallback on reload failure is the wrong channel for emergency revocation — operational revocation must flow through the CA pool / CRL, not leaf-file deletion. SEC-047 (LOW): added `TestCertCache_BadReloadFallsBackToCached` regression that pins the documented behaviour when the new cert file is malformed PEM, plus `slog.Warn` on both the stat-failure-with-cache and the reload-failure-with-cache branches so a stuck rotation is visible in the log. Both findings surfaced by the pre-PR 6.9 security-agent batch.
>
> Last updated: 2026-06-30 — SEC-043 RESOLVED in the same `fix/sec-040-041-042-sso-followups` branch (fix-up commit on top of `5ece50a`). Both handlers now dispatch on `service.ErrSSOSubjectMismatch` → `401 UNAUTHORIZED` with the fixed SEC-042 generic body; new `TestSSOCallback_SEC043_SubjectMismatchReturns401WithGenericBody` regression covers the OAuth path end-to-end (drives a second callback with a mutated `sub`, asserts 401 + generic body + no email leak in body OR redirect Location). SEC-041 guard tightened — now refuses whenever `ident.Subject != "" && byEmail.SSOSubject != ident.Subject`, dropping the `!= ""` precondition so an unexpected empty-subject race-winning row fails closed rather than handing back a session.
>
> Last updated: 2026-06-30 — SEC-043 logged (MEDIUM, blocker for `fix/sec-040-041-042-sso-followups` PR). Service-layer fixes for SEC-040/041/042 are correct, but neither the OAuth (`services/auth/internal/handler/sso.go:304`) nor the SAML (`services/auth/internal/handler/saml.go:315`) handler maps `service.ErrSSOSubjectMismatch`, so a subject-mismatch SSO callback now surfaces as `500 INTERNAL` (with the bare error logged at ERROR) instead of a clean `401 UNAUTHORIZED`. The email-enumeration objective of SEC-042 is still met (the email no longer rides the wire), but the user-facing posture and operator-log signal both regress. Also flagged: SEC-041 fix only fires when `byEmail.SSOSubject != ""`; if a parallel callback writes the row with an empty subject, this caller is still handed a session without subject verification (narrower than the original SEC-041 window but the same shape).
>
> Last updated: 2026-06-29 — SEC-040/041/042 logged (all MEDIUM/LOW from Phase 5.5 SSO subject-binding review) — `GetUserBySSOSubject` missing tenant filter (multi-mode boundary blur), race-recovery skips subject-mismatch reconciliation, and rejection error message leaks "account exists for email X" (email enumeration). Accepted as should-fix follow-ups so PR #195 could ship per cadence; all three remain OPEN.
>
> Last updated: 2026-06-29 — SEC-039 logged + RESOLVED (HIGH, same shape as SEC-038 across services/core, scanner, proxy, management — 17 dial sites missing serverName pin + silent insecure fallback on TLS load error; fixed by commit `41e9a72` on branch `fix/sec-039-clientcreds-sweep`, PR pending).
> Last updated: 2026-06-29 — SEC-038 (MEDIUM) RESOLVED in commit `329c63b` on branch `fix/sec-038-gc-clientcreds` (per-target serverName pinning + fail-closed mTLS load in `services/gc`). SEC-039 (MEDIUM) logged — same `clientCreds(cfg)` shape (empty serverName + insecure fallback on TLS error) still present in `services/core`, `services/scanner`, `services/proxy`, and `services/management`.
> Last updated: 2026-06-29 — SEC-038 logged (MEDIUM, services/gc reuses a shared mTLS client without pinning the `registry-tenant` server name when dialling for Phase 3.4 bootstrap fetch; client-side TLS load failure also silently downgrades to plaintext) from Phase 3.4 SingleTenantInjector rollout review (PRs #170–#179).
>
> Last updated: 2026-06-27 — SEC-037 logged (LOW, single-statement onboarding backfill could contend with login UPDATEs on large user tables) from Phase 4.3 §1 review of commit `ec43e05`.
>
> Last audited: 2026-06-21 — Round-3 PENTEST-029 / 031 / 032 verified RESOLVED in the codebase; PENTEST-033 verified PARTIAL (login password now `{{password}}` secret-typed env var, but `NewUser1234!` still inlined in createUser body and dev tenant UUID still defaulted in environment file). PENTEST-030 remains OPEN (no per-endpoint test-dispatch throttle yet).
>
> Last updated: 2026-06-19 (SEC-001..SEC-036 all resolved; PENTEST-001..026 all resolved. **Round 3 (2026-06-19):** post-merge review of FE-API-001/010/021..024 + the 00004 manifest backfill migration on branch `feat/frontend-rebuild` — 7 new findings (0 critical, 2 high, 3 medium, 2 low). **PENTEST-027 + PENTEST-028 (both HIGH) resolved same day** — webhook list/deliveries routes gated by `requireWebhookAdmin`; dispatcher errors sanitised so persisted `last_error` never carries URL-embedded tokens; manifest backfill split out of the migration into an idempotent `psql` runbook with a high-water-mark cursor + per-batch commits. PENTEST-029..033 (3 medium + 2 low) remain OPEN as follow-ups.)
> This file tracks all known security issues, findings, and open remediations across the platform.
> Sensitive details (CVEs, exploit paths) should not be committed here — link to a private issue tracker for those.

---

## Legend

| Severity | Meaning |
|---|---|
| `CRITICAL` | Exploitable now, immediate remediation required |
| `HIGH` | Significant risk, fix before next release |
| `MEDIUM` | Moderate risk, fix within current sprint |
| `LOW` | Minor risk, fix when convenient |
| `INFO` | Informational, no direct risk |

| Status | Meaning |
|---|---|
| `OPEN` | Not yet addressed |
| `IN PROGRESS` | Being remediated |
| `MITIGATED` | Workaround applied, full fix pending |
| `RESOLVED` | Fixed and verified |
| `ACCEPTED` | Risk accepted with documented rationale |
| `WONT FIX` | Out of scope, documented reason required |

---

## Open Issues

> _[SUPERSEDED by the current-open banner at the top of this file — SEC-057..062/064..070 have since resolved and SEC-071..075 were added.]_ 15 OPEN SEC findings as of 2026-07-02 — SEC-057 (HIGH, FUT-001 issuer allowlist prefix bug) + SEC-058/059/060 (MEDIUM, FUT-001 JWKS SSRF + OOM + TTL bounds) + SEC-061/062 (LOW, FUT-001 rate-limit key + client timeouts) + SEC-063 (INFO, FUT-001 HTTPS scheme not enforced BE-side) + SEC-066 (MEDIUM, PutTokenPolicy trusts caller tenant_id — PeerTenantCheck interceptor is the durable fix) + SEC-053/054 (LOW, spec-lint hardening) + SEC-055/056 (LOW, FUT-002 follow-ups) + SEC-037 (LOW, migration backfill lock) + SEC-048 (LOW, JWT fallback unmetered) + SEC-049 (INFO, keyring kid provenance). Plus PENTEST-030 (OPEN) + PENTEST-033 (PARTIAL). RESOLVED 2026-07-01/02: SEC-064/067 (PR #226), SEC-065/068 (PR #228), SEC-069/070 (`fix/sec-068-access-review-tenant-scoping`). See SEC-NNN entries below for full triage notes.
>
> Backend feature gaps (KMS signing backends, Notary v2, etc.) are tracked in
> `status.md` Sprint 6 — those are unimplemented features rather than
> security regressions, so they live in the project tracker, not here.

### SEC-040 — `GetUserBySSOSubject` missing tenant filter
- **Severity:** MEDIUM
- **Status:** RESOLVED
- **Service:** `services/auth`
- **Raised:** 2026-06-29 (Phase 5.5 review on PR #195)
- **Resolved:** 2026-06-30 — branch `fix/sec-040-041-042-sso-followups`. Migration `20260630120000_users_sso_subject_tenant_filter.sql` drops the global `(provider, subject)` partial index and recreates as `idx_users_sso_subject_tenant` over `(tenant_id, sso_provider_id, sso_subject) WHERE sso_subject IS NOT NULL`. `GetUserBySSOSubject` signature gains `tenantID`; `EnsureSSOUser` threads `resolvedTenantID` through both call sites (Step 1 fast-path + race-recovery fallback). Regression test `TestSSO_SEC040_TenantFilterOnSubjectLookup` confirms the same `(provider, subject)` tuple in two tenants now produces two distinct users.
- **Description:** The Phase 5.5 partial-index lookup `idx_users_sso_subject ON users (sso_provider_id, sso_subject) WHERE sso_subject IS NOT NULL` is global, not tenant-scoped. `GetUserBySSOSubject(ctx, providerID, subject)` matches purely on `(provider_id, subject)`. In single-tenant mode this is fine — there is only one tenant — but `DEPLOYMENT_MODE=multi` keeps the existing SaaS posture, and if two tenants ever shared an IdP (e.g. both use Google Workspace OAuth), a recycled subject id could surface a user from the wrong tenant.
- **Remediation:** Add `tenant_id` filter to the lookup and to the partial-index definition. Existing migration `20260629222534_users_sso_subject.sql` can be amended via a follow-up migration that drops + recreates the index with the tenant column. The `EnsureSSOUser` call site already has `tenantID` in scope — propagate it through.
- **References:** REDESIGN-001 Phase 5.5 (`.claude/plans/2026-06-26-single-tenant-redesign.md`), CLAUDE.md §9 (tenant isolation), CWE-639 (Authorization Bypass Through User-Controlled Key).

### SEC-041 — SSO race-recovery skips subject-mismatch reconciliation
- **Severity:** LOW
- **Status:** RESOLVED
- **Service:** `services/auth`
- **Raised:** 2026-06-29 (Phase 5.5 review on PR #195)
- **Resolved:** 2026-06-30 — branch `fix/sec-040-041-042-sso-followups`. `EnsureSSOUser` now re-verifies the recovered row's subject on the email-fallback recovery path; rejection wraps `ErrSSOSubjectMismatch` with the SEC-042 generic body. Guard tightened per security-agent review: refuses whenever `ident.Subject != "" && byEmail.SSOSubject != ident.Subject` — empty-subject row no longer earns a free pass. Regression test `TestSSO_SEC041_RaceRecoveryRefusesSubjectMismatch` exercises the path via a `failCreateSSOWith` injection.
- **Description:** When two concurrent SSO logins for the same subject hit `CreateSSOUser` and one wins the unique-index race, the loser falls back to `GetUserBySSOSubject(ctx, providerID, subject)`. The recovered row is returned without re-verifying that its `sso_subject` actually equals the requested subject. The race window is narrow (concurrent first-login of the same identity is exceptional), but if a buggy IdP or test harness emits two different subjects for the same email in quick succession, the loser could be handed a row whose subject was set by the winner's auth context.
- **Remediation:** After the recovery query returns, check `loadedUser.SSOSubject == subject`; if mismatched, return a clear "subject mismatch — contact your admin" error instead of returning a session for the wrong identity. Single statement, no schema change.
- **References:** REDESIGN-001 Phase 5.5, CWE-362 (Concurrent Execution using Shared Resource with Improper Synchronization).

### SEC-042 — SSO rejection message leaks "account exists for email X" — email enumeration
- **Severity:** LOW
- **Status:** RESOLVED
- **Service:** `services/auth`
- **Raised:** 2026-06-29 (Phase 5.5 review on PR #195)
- **Resolved:** 2026-06-30 — branch `fix/sec-040-041-042-sso-followups`. Generic rejection body: "this SSO identity is not linked to a registered account — contact your admin to link it." Email stripped from all wrapped errors on the SSO path (the well-trodden subject-mismatch branch, the race-recovery email-fallback, and two `lookup human user...` wrap sites). Server-side `slog.WarnContext` still emits the email for operator debugging. SEC-043 (companion finding) ensures both the OAuth and SAML handlers actually dispatch on `ErrSSOSubjectMismatch` so the generic body reaches the wire. Regression coverage: `TestSSO_RecycledEmail_Rejected` updated with `NotContains(err.Error(), email)` assertion + `TestSSOCallback_SEC043_SubjectMismatchReturns401WithGenericBody` covers the full HTTP path.
- **Description:** When an SSO login arrives with a fresh `subject` but the email already maps to a different existing user, `EnsureSSOUser` rejects with a message that echoes the email back ("An account exists for `<email>`; ask your admin to link it to your new SSO identity"). An attacker controlling an IdP they can spin up freely (Auth0 trial, self-hosted Keycloak) can probe which email addresses are registered with the deployment by issuing logins for emails of interest and reading the rejection string.
- **Remediation:** Collapse to a generic message that does not echo the email: "This SSO identity is not linked to a registered account — contact your admin to link it." Server-side log can still include the email (we control that audience). Same shape as PENTEST-005's "collapse auth failure variants into one 401" rule applied at the SSO surface.
- **References:** REDESIGN-001 Phase 5.5, OWASP Authentication Cheat Sheet (account enumeration), CWE-204 (Observable Response Discrepancy).

### SEC-043 — SSO handlers don't map `ErrSSOSubjectMismatch`; mismatch surfaces as `500 INTERNAL`
- **Severity:** MEDIUM
- **Status:** RESOLVED (same branch)
- **Service:** `services/auth`
- **Raised:** 2026-06-30 (review of `fix/sec-040-041-042-sso-followups`, commit `5ece50a`)
- **Description:** The service-layer fixes for SEC-040/041/042 in `services/auth/internal/service/sso.go` are correct — the rejection error wraps `ErrSSOSubjectMismatch` with a generic, non-enumerating message. But neither HTTP handler `errors.Is`-dispatches on that sentinel:
  - `services/auth/internal/handler/sso.go:304-318` (OAuth callback) only switches on `ErrEmailNotVerified`, `ErrAutoProvisionDisabled`, `ErrAccountDisabled` then falls through to `slog.ErrorContext("sso: EnsureSSOUser", "err", err)` + `writeError(500, "INTERNAL", "internal error")`.
  - `services/auth/internal/handler/saml.go:315-334` (SAML ACS) has the same shape and the same default branch.

  Consequences:
  1. A legitimate recycled-email/mismatch rejection — the well-trodden case SEC-042 was raised against — now returns `500 INTERNAL` instead of a clean `401 UNAUTHORIZED`. End-users see a generic server error and have nothing actionable. The SEC-042 generic message (`this SSO identity is not linked to a registered account — contact your admin to link it`) is built but never rendered.
  2. The server-side log line becomes `slog.ErrorContext("sso: EnsureSSOUser", "err", "sso subject does not match the persisted binding for this email: this SSO identity is not linked..."` — alert-noise (ERROR level) for what is an expected client-side authentication outcome, not a server fault.
  3. Subtle: the `ErrSSOSubjectMismatch.Error()` string contains the phrase "the persisted binding for this email" — when the wrapped error is logged at ERROR level on every mismatch, operators reading the log get the email correlated via the structured field anyway, but the SLO/alert pipeline now treats every email-enumeration probe as a server outage.

  Regression tests for SEC-040/041/042 (`TestSSO_SEC040_TenantFilterOnSubjectLookup`, `TestSSO_SEC041_RaceRecoveryRefusesSubjectMismatch`, `TestSSO_RecycledEmail_Rejected`) all assert at the **service** layer (`require.ErrorIs(err, ErrSSOSubjectMismatch)`) — they do not exercise the HTTP handler dispatch, so the gap is not caught by CI.

  Secondary (smaller) gap on SEC-041: the race-recovery mismatch check at `sso.go:510` is gated on `byEmail.SSOSubject != ""`. If a parallel callback created the row through a path that left `sso_subject` empty (e.g. a future code path that backfills subject lazily), the caller still gets a session for that row without subject verification. The current code only creates rows with subject set so the window is closed in practice, but the guard isn't strictly tight.
- **Remediation:**
  1. Add an `errors.Is(err, service.ErrSSOSubjectMismatch)` case to **both** `services/auth/internal/handler/sso.go` (OAuth callback) and `services/auth/internal/handler/saml.go` (SAML ACS). Map to `writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "<generic SEC-042 message>")` — do NOT propagate `err.Error()`, render a fixed string so the wrapped tail can't leak.
  2. Drop the log level for this branch to `slog.WarnContext` (it's a client-side authentication outcome, not a server fault).
  3. Add handler-level regression tests in `services/auth/internal/handler/sso_test.go` and `saml_test.go` that drive an SSO callback with a mismatched subject and assert `401 UNAUTHORIZED` + body string contains the generic phrasing + body does NOT contain the email.
  4. Tighten the SEC-041 guard to refuse whenever `ident.Subject != "" && byEmail.SSOSubject != ident.Subject` (drop the `byEmail.SSOSubject != ""` precondition; empty-subject race-recovery should still re-bind via `SetSSOSubject` after verifying no other row in the tenant already claims `ident.Subject`).
- **References:** SEC-040, SEC-041, SEC-042, CLAUDE.md §7 (HTTP Bearer Auth — clean 401 vs 500), CWE-755 (Improper Handling of Exceptional Conditions), CWE-209 (Information Exposure Through an Error Message — partial; the email itself is stripped, but the 500 status code is itself a signal).

### SEC-057 — OIDC issuer allowlist uses raw `HasPrefix`; attacker-registered subdomain bypasses the gate
- **Severity:** HIGH
- **Status:** RESOLVED
- **Service:** `services/auth`
- **Raised:** 2026-07-01 (pre-PR review of `feat/fut-001-federated-workload-identity`)
- **Resolved:** 2026-07-02 — `fix/sec-057-062-oidc-jwks-hardening`. The core bypass (remediation #1) was already closed in-code: `issuerAllowed` requires the char after the matched prefix to be `/` or end-of-string, with regression tests for `.evil.com` / `-evil.com` / subdomain-lookalike suffixes (`oidc_issuer_test.go`). This branch adds remediation #2 — `parseIssuerAllowlist` now drops scheme-less allowlist entries (config-typo guard; a bare host can never prefix-match an `https://` issuer, so the entry is dead weight). **Remediation #3 (trailing slashes on the docker-compose defaults) was deliberately NOT applied and is withdrawn as incorrect:** the real IdP `iss` claims are slash-less (GitHub Actions is exactly `https://token.actions.githubusercontent.com`), and a trailing-slash allowlist entry is a strict prefix of nothing the IdP sends — `strings.HasPrefix(slashlessIssuer, slashedPrefix)` is false — so it would reject every legitimate token. The in-code boundary check is the correct and sufficient fix; the compose comment now documents why trailing slashes must not be added.
- **Description:** `services/auth/internal/service/oidc_issuer.go:27-44` implements the `OIDC_ALLOWED_ISSUERS` gate as `strings.HasPrefix(issuer, prefix)` with no boundary check. If the operator sets `OIDC_ALLOWED_ISSUERS=https://token.actions.githubusercontent.com` (the exact string the operator would paste from GitHub's OIDC docs — no trailing slash), an attacker who can register `token.actions.githubusercontent.com.evil.example` and stand up an OIDC IdP there will pass the allowlist because `strings.HasPrefix("https://token.actions.githubusercontent.com.evil.example", "https://token.actions.githubusercontent.com")` returns true. The attacker then hosts a well-formed discovery + JWKS document, mints an RS256 token with matching `iss`, `sub`, `aud` values, and drives the exchange to `IssueWorkloadToken` — minting a 15-minute registry JWT for whichever SA the trust maps to. Compounded by `infra/docker-compose/docker-compose.yml:373` shipping three vulnerable defaults out-of-the-box: `https://token.actions.githubusercontent.com,https://gitlab.com,https://agent.buildkite.com` — every one is a raw origin with no trailing slash. Attack still requires an existing trust in the DB, so exploitability is limited to workspaces that have already federated (an admin action). But once federated, any external domain attacker who can register `gitlab.com.*` gets an OIDC-mint bypass without touching the workspace's IdP.
- **Remediation:**
  1. In `issuerAllowed`, treat each allowlist entry as a URL prefix that ends at a `/` boundary: require the character AT `len(prefix)` in `issuer` to be either the end-of-string or `/`. Concretely: `if strings.HasPrefix(issuer, prefix) && (len(issuer) == len(prefix) || issuer[len(prefix)] == '/') { return true }`.
  2. Reject prefixes without an explicit scheme (`http://`|`https://`) at `ParseIssuerAllowlist` time — an entry like `token.actions.githubusercontent.com` (bare host) is almost certainly a config typo.
  3. Update `infra/docker-compose/docker-compose.yml:373` to add trailing slashes to every default entry: `https://token.actions.githubusercontent.com/,https://gitlab.com/,https://agent.buildkite.com/`.
  4. Extend `TestIssuerAllowed` with the boundary case: `{"subdomain suffix rejected", []string{"https://token.actions.githubusercontent.com"}, "https://token.actions.githubusercontent.com.evil.example", false}`.
- **References:** CLAUDE.md §7 (Input Validation — allowlist gates), CWE-20 (Improper Input Validation), CWE-284 (Improper Access Control), OWASP ASVS 4.0 V13.2.3 (open-redirect / trust-boundary prefix confusion).

### SEC-058 — JWKS SSRF: `jwks_uri` from discovery doc followed without host/scheme constraint
- **Severity:** MEDIUM
- **Status:** RESOLVED
- **Service:** `services/auth`
- **Raised:** 2026-07-01 (pre-PR review of `feat/fut-001-federated-workload-identity`)
- **Resolved:** 2026-07-02 — `fix/sec-057-062-oidc-jwks-hardening`. New `validateJWKSURI` (`oidc_jwks.go`) pins the discovery-supplied `jwks_uri` to the issuer's ORIGIN — same scheme AND same host (case-insensitive, port-inclusive) — before any fetch. Chose same-origin over the finding's literal `scheme == "https"` because it is stricter (blocks scheme downgrade + exotic schemes) and does not break http test/dev IdPs, while still requiring https transitively for real https issuers (SEC-063 enforces https issuer_url at trust-create). The JWKS `http.Client` also now sets `CheckRedirect = ErrUseLastResponse`, so a 30x can't chase us onto an internal endpoint (`getJSON` sees the non-200 and errors). Regression test `TestJWKS_SSRF_ForeignHostRejected` asserts a foreign-host `jwks_uri` is rejected and the victim host is never contacted. Note the private-IP blocklist (remediation #3) is subsumed: same-host-as-a-public-issuer cannot resolve to an RFC-1918 address unless the allowlisted issuer itself is internal (an operator choice).
- **Description:** `services/auth/internal/service/oidc_jwks.go:131-189` reads the issuer's `/.well-known/openid-configuration`, extracts the `jwks_uri` string field, and calls `getJSON` on it (line 148). No validation is performed on the returned URL — the attacker-controlled discovery doc can set `jwks_uri` to any URL, including an internal RFC-1918 address or a cloud metadata endpoint. Realistic exploit paths:
  1. **Legitimate-but-compromised IdP** — an IdP with a compromised discovery endpoint (or an operator-configured mis-issued cert) redirects `jwks_uri` at `http://169.254.169.254/latest/meta-data/iam/security-credentials/` (AWS IMDS). Our `getJSON` fetches, gets a plaintext response, JSON-decodes it (fails on plaintext but the request is sent), and the auth service has just leaked its outbound IP + potentially triggered a credential lookup.
  2. **Redirects** — Go's default `http.Client` follows up to 10 redirects; the initial JWKS URI can 302 to an internal endpoint.
  3. **Combined with SEC-057** — an attacker who bypasses the issuer allowlist controls the discovery doc entirely, so `jwks_uri` can point at anything.

  The prior `webhook` service already implements a private-IP blocklist (per CLAUDE.md §17.5); the same primitive should apply here.
- **Remediation:**
  1. Validate `jwks_uri` against the issuer origin: parse both URLs and require `jwksURL.Scheme == "https"` AND `jwksURL.Host == issuerURL.Host`. Realistic IdPs host the JWKS on the same host as the discovery endpoint; any deviation is a red flag.
  2. Disable client redirects on the JWKS client: `Client.CheckRedirect = func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }`.
  3. Reuse the webhook SSRF helper (`libs/net/ssrf`) if it exists, or inline a private-IP blocklist for 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 127.0.0.0/8, 169.254.0.0/16, ::1.
  4. Extend `oidc_jwks_test.go` with a stub IdP that returns a `jwks_uri` pointing at a different host, and assert `Fetch` returns an error.
- **References:** CLAUDE.md §17.5 (SSRF checks + private-IP blocklist), CWE-918 (Server-Side Request Forgery), OWASP Top-10 A10 (SSRF).

### SEC-059 — JWKS + discovery HTTP responses have no size cap → OOM DoS via a hostile IdP
- **Severity:** MEDIUM
- **Status:** RESOLVED
- **Service:** `services/auth`
- **Raised:** 2026-07-01 (pre-PR review of `feat/fut-001-federated-workload-identity`)
- **Resolved:** 2026-07-02 — `fix/sec-057-062-oidc-jwks-hardening`. `getJSON` now reads the body through `io.LimitReader(resp.Body, jwksMaxResponseBytes+1)` and errors if the read exceeds `jwksMaxResponseBytes` (1 MiB), covering BOTH the discovery and JWKS fetches (they share `getJSON`). The `+1` makes a body exactly at the cap succeed while an oversize one is rejected rather than silently truncated. Regression test `TestJWKS_OversizeResponse_Rejected` streams >1 MiB and asserts the fetch errors. Deep-nesting allocation-storm (remediation #3, optional) is not addressed here — the 1 MiB cap bounds the worst case; a JSON-depth limit remains a nice-to-have follow-up.
- **Description:** `services/auth/internal/service/oidc_jwks.go:207` reads the response body via `json.NewDecoder(resp.Body).Decode(&out)` with no `io.LimitReader` wrap. `resp.Body` streams unbounded — a hostile (or compromised) IdP can serve a discovery doc or JWKS document of arbitrary size (multi-GB or infinite). The 5-second `Timeout` on the HTTP client caps wall-clock time but not memory: within 5 seconds an attacker on a fast link can push hundreds of MB. Combined with the 16-issuer cache cap (each entry holds parsed keys), the attacker can also inflate the JSON structure (deeply nested arrays) to trigger `encoding/json` allocation storms. No workaround at the caller — this must be fixed inside `getJSON`.
- **Remediation:**
  1. Wrap `resp.Body` with `io.LimitReader(resp.Body, jwksMaxResponseBytes)` where `jwksMaxResponseBytes = 1 << 20` (1 MiB). A realistic JWKS with 10 keys is ~5 KiB; 1 MiB is 200× headroom without letting a single response drive OOM.
  2. Return a clear error `"jwks response exceeded 1 MiB"` when the read hits the cap.
  3. Optional but recommended: cap the JSON decoder depth via a custom `json.Decoder` with a max-depth check (mitigates the deep-nesting allocation storm class).
  4. Add `TestJWKS_OversizeResponse_Rejected` — stub IdP serves 2 MiB of `{"junk": "..."}` and the fetch must error.
- **References:** CLAUDE.md §13 (Request body size limits), CWE-400 (Uncontrolled Resource Consumption), CWE-770 (Allocation of Resources Without Limits).

### SEC-060 — `JWKSCacheTTLSeconds` has no min/max bound; attacker-admin can force JWKS refetch storm
- **Severity:** MEDIUM
- **Status:** RESOLVED
- **Service:** `services/auth`
- **Raised:** 2026-07-01 (pre-PR review of `feat/fut-001-federated-workload-identity`)
- **Resolved:** 2026-07-02 — `fix/sec-057-062-oidc-jwks-hardening`. New `validateJWKSCacheTTL` rejects `JWKSCacheTTLSeconds` outside [60, 86400], wired into BOTH `validateOnCreate` and `Update`. `0` passes through untouched (the repository layer maps it to the 3600s default, preserving existing callers). Regression subtests in `TestOIDCTrustService_Create_Validations` cover 0 (accepted), 59 (rejected), −1 (rejected), 86401 (rejected), and the 60/86400 bounds (accepted). The optional CHECK constraint (remediation #2) was not added — the service-layer gate is the single write path; a migration-level constraint is a nice-to-have backstop tracked for the next auth migration.
- **Description:** `services/auth/internal/service/oidc_trust.go:130-181` (Create + Update) never validates `JWKSCacheTTLSeconds`. Repository layer at `repository/oidc_trust.go:67-69` and `:203-205` only defaults 0 → 3600. Downstream `oidc_jwks.go:83-84` computes `ttl := time.Duration(matched.JWKSCacheTTLSeconds) * time.Second` and gates `time.Since(entry.fetchedAt) < ttl`. Two attack shapes:
  1. **TTL = negative or 0** — `ttl == 0` means `time.Since(...) < 0` is always false → every exchange refetches JWKS. A tenant-admin (whose account has been compromised, or a malicious insider) can set TTL to `-1` on every trust and turn our auth service into a JWKS-request amplifier against the upstream IdP (compromising us with GitHub / GitLab).
  2. **TTL = MaxInt32 (~68 years)** — the cache retains keys indefinitely. Combined with the SEC-058 SSRF, this lets an attacker who transiently controls the JWKS endpoint pin their key for the lifetime of the deployment.

  Sane bounds per OIDC industry practice: 60s min (IdPs typically publish TTLs of 300s–3600s; 60s tolerates operators dropping to a low value for a temporary IdP rotation), 86400s max (24h — beyond this and a legit IdP key rotation is missed).
- **Remediation:**
  1. In `oidc_trust.go` `validateOnCreate` and `Update`, reject `JWKSCacheTTLSeconds < 60 || JWKSCacheTTLSeconds > 86400`. Preserve the `== 0 → default 3600` shortcut for backwards compat with the repo's own defaulting.
  2. Consider adding a CHECK constraint `CHECK (jwks_cache_ttl_seconds BETWEEN 60 AND 86400)` in the migration (belt-and-braces).
  3. Extend `oidc_trust_test.go` with `TestCreate_RejectsExtremeTTL`.
- **References:** CLAUDE.md §7 (Input Validation), CWE-1284 (Improper Validation of Specified Quantity in Input), CWE-770 (rate-limit + resource-exhaustion class).

### SEC-061 — Workload rate-limit Redis key has no bound on untrusted `sub` claim length
- **Severity:** LOW
- **Status:** RESOLVED
- **Service:** `services/auth`
- **Raised:** 2026-07-01 (pre-PR review of `feat/fut-001-federated-workload-identity`)
- **Resolved:** 2026-07-02 — `fix/sec-057-062-oidc-jwks-hardening`. New `workloadRateLimitKey(iss, sub)` (remediation #2, the hashing option) returns `"workload:rate:" + hex(sha256(iss + "\x00" + sub))` — a fixed 64-hex-char key regardless of claim length, so a multi-MB `sub` can no longer bloat Redis. The NUL separator also resolves the `:`-ambiguity flagged in the original comment. Regression test `TestWorkloadRateLimitKey` asserts constant key length under a 5 MiB subject, separator disambiguation, and determinism.
- **Description:** `services/auth/internal/handler/http_workload_token.go:195` builds `key := "workload:rate:" + iss + ":" + sub` from `peekIssuerAndSubject` — the raw claims of an unverified JWT. Neither `iss` nor `sub` is capped at that point (the 256-char cap in `oidc_exchange.go` is for the audit log, not the Redis key). A hostile caller can submit an unsigned JWT with a `sub` of megabytes → the Redis key becomes megabytes; every request adds ~$sub bytes to Redis memory (per bucket TTL 60s). Not a bypass of the rate limit (the attacker only bloats their own bucket), but a controllable Redis-memory consumption vector — a hostile client can allocate ~100 MiB in Redis for the cost of ~50 exchange requests. Mitigated by the 60s TTL and by Redis eviction policies (`maxmemory-policy`) but shouldn't be left unbounded.
- **Remediation:**
  1. Truncate `iss` and `sub` to the same 256-char cap `maxSubjectLogLen` before building the key. Rate-limit collisions on truncated buckets are acceptable — a subject longer than 256 chars is almost certainly not a legitimate CI runner.
  2. Alternatively, hash the (iss, sub) tuple: `key := "workload:rate:" + hex.EncodeToString(sha256(iss + "\x00" + sub))` — fixed 64-byte key regardless of claim length, plus resolves the `:`-separator ambiguity noted in the comment.
  3. Add `TestWorkloadHandler_LongSubjectDoesNotBloatKey`.
- **References:** CLAUDE.md §7 (Input Validation), CWE-770 (Allocation of Resources Without Limits or Throttling).

### SEC-062 — JWKS HTTP client only sets `Timeout`, missing `TLSHandshakeTimeout` + `ResponseHeaderTimeout`
- **Severity:** LOW
- **Status:** RESOLVED
- **Service:** `services/auth`
- **Raised:** 2026-07-01 (pre-PR review of `feat/fut-001-federated-workload-identity`)
- **Resolved:** 2026-07-02 — `fix/sec-057-062-oidc-jwks-hardening`. `NewOIDCTrustService` now builds the JWKS client with an explicit `http.Transport` setting `TLSHandshakeTimeout: 3s` + `ResponseHeaderTimeout: 3s` alongside the 5s overall `Timeout`, so a stalled handshake or slow header-write cannot silently consume the full request budget (CLAUDE.md §13).
- **Description:** `services/auth/internal/service/oidc_trust.go:94` constructs `&http.Client{Timeout: 5 * time.Second}` with no explicit `Transport`. CLAUDE.md §13 requires all three timeouts to be set explicitly (`Timeout`, `TLSHandshakeTimeout`, `ResponseHeaderTimeout`) so a stalled TLS handshake or a slow header-write can't burn the full request budget silently. The 5-second wall-clock cap does provide an overall bound, so the risk is graduated — but the hardening rule exists precisely because operators reading `Timeout: 5s` don't realise a hostile server can consume 4.9s of TLS handshake before sending the first response byte.
- **Remediation:**
  1. Replace the `&http.Client{Timeout: 5 * time.Second}` literal with a fully-configured client:
     ```go
     jwksClient := &http.Client{
       Timeout: 5 * time.Second,
       Transport: &http.Transport{
         TLSHandshakeTimeout:   3 * time.Second,
         ResponseHeaderTimeout: 3 * time.Second,
         MaxIdleConns:          10,
         IdleConnTimeout:       90 * time.Second,
       },
     }
     ```
  2. Consider factoring this into `libs/net/httpclient` if a matching helper doesn't already exist.
- **References:** CLAUDE.md §13 (HTTP clients: always set timeouts).

### SEC-063 — Backend does not enforce HTTPS scheme on `issuer_url`; FE-only defense
- **Severity:** INFO
- **Status:** OPEN
- **Service:** `services/auth`
- **Raised:** 2026-07-01 (pre-PR review of `feat/fut-001-federated-workload-identity`)
- **Description:** `frontend/src/components/access/CreateOIDCTrustDialog.tsx:123` enforces `issuerUrl.startsWith("https://")` client-side. The backend at `services/auth/internal/service/oidc_trust.go:validateOnCreate` and `oidc_issuer.go:issuerAllowed` accepts any scheme — a caller who bypasses the FE (curl with a bearer token) can register a trust with `http://` and the auth service will fetch the discovery + JWKS in plaintext. Currently not exploitable because the operator would also have to add the same `http://` prefix to `OIDC_ALLOWED_ISSUERS` env, which is an explicit operator opt-in. Defence-in-depth gap only — but the FE dialog's implicit contract ("BE agrees HTTPS is required") is broken.
- **Remediation:**
  1. In `validateOnCreate`, reject `!strings.HasPrefix(in.IssuerURL, "https://")` before the allowlist check. Same rule in `Update` if we ever open `issuer_url` for mutation.
  2. Optionally reject `http://` entries at `ParseIssuerAllowlist` time (see SEC-057 remediation #2).
- **References:** CLAUDE.md §17 (Secrets/network hardening — no plaintext egress), OWASP ASVS V9.1 (TLS everywhere), CWE-319 (Cleartext Transmission of Sensitive Information).

### SEC-064 — `CreateAPIKey` skips workspace `max_ttl_days` cap when caller omits `expires_at`
- **Severity:** HIGH
- **Status:** RESOLVED
- **Service:** `services/auth`
- **Raised:** 2026-07-01 (pre-PR review of `feat/fut-003-token-policies`)
- **Resolved:** 2026-07-01 — PR #226 (`fix(fut-003): SEC-064 HIGH + CR routing bug + SEC-067 no-op audit`). Remediation option A: nil `expiresAt` is clamped to `now + max_ttl_days` when a policy cap is set (the cap becomes the default). Regression test `TestCreateAPIKey_NilExpiryClampedToPolicyCap` verified failing on pre-fix code. **Residual:** the SA branch (`service_account.go` `IssueKey`, remediation #3) was NOT included — SA keys remain perpetual per the documented v1 spec limitation; revisit if the SA-key FE flow gains an expiry input.
- **Description:** `services/auth/internal/service/auth.go:657` guards the max-TTL check with `if policy.MaxTTLDays != nil && expiresAt != nil`. When the HTTP request body omits `expires_at` (`services/auth/internal/handler/http.go:588` `ExpiresAt *time.Time`), the pointer is nil and the entire enforcement clause is skipped. The resulting `api_keys` row has `expires_at IS NULL`, which downstream `ValidateAPIKey` at `auth.go:802` treats as "no expiry — key valid forever". Net effect: an operator who sets a strict `max_ttl_days=30` workspace cap is trivially bypassed by any caller (human user, curl script, or the FE happy path if the user leaves the "expiry" input blank) simply by omitting the field. The load-bearing FUT-003 promise — "no API key may have a lifetime longer than this value" — is broken. Grandfathering tests (`token_policy_test.go:363`) do not catch this because they always pass an explicit `expiresAt`. The idle-revoke worker partially mitigates by revoking never-used keys after `idle_revoke_days` — but that is a separate policy dimension and can be independently disabled. Also affects FUT-003 rotation: when `expires_at` is nil and `rotation_interval_days` is set, `rotationDueAt` is still stamped (correctly), so rotation lapse is enforced — but the underlying TTL bypass remains.
- **Remediation:**
  1. In `CreateAPIKey`, when `policy.MaxTTLDays != nil` treat a nil `expiresAt` as "clamp to the cap": stamp `expiresAt = time.Now().Add(time.Duration(*policy.MaxTTLDays) * 24 * time.Hour)` (preferred — the cap becomes the default). Alternative: reject with `InvalidArgument` if `expiresAt == nil` under a configured cap ("expiry required by workspace policy"), pushing the choice back to the caller.
  2. Add a regression test in `services/auth/internal/service/token_policy_test.go` that sets `MaxTTLDays=30`, calls `CreateAPIKey` with `expiresAt=nil`, and asserts either the returned key has `ExpiresAt` populated (option A) or the call returns `codes.InvalidArgument` (option B).
  3. Same guard needed on `services/auth/internal/service/service_account.go:585` `ServiceAccountService.IssueKey` — SA keys are perpetual by design today (per spec: "workspace-wide only in v1"), but the caller-facing FE flow (`ServiceAccountDetail.tsx`) also lets an operator issue an SA key with no expiry, silently bypassing the workspace cap for the SA branch. Accept as follow-up (documented spec limitation) or fix in this PR.
- **References:** CLAUDE.md §7 (Input Validation — allowlist enforcement at handler layer), CWE-269 (Improper Privilege Management), CWE-841 (Improper Enforcement of Behavioral Workflow).

### SEC-065 — Client-side `idle_revoke_days` floor (7 days) not enforced by FE `PoliciesPanel`
- **Severity:** LOW
- **Status:** RESOLVED
- **Service:** `frontend/`
- **Raised:** 2026-07-01 (pre-PR review of `feat/fut-003-token-policies`)
- **Resolved:** 2026-07-01 — PR #228. New `PER_FIELD_MIN` table maps `idle_revoke_days` → 7; `validateSection` resolves the per-dimension floor; numeric input `min=` mirrors it; regression test pins "type 3 → save → inline banner + mutation NOT called".
- **Description:** `services/auth/internal/service/token_policy.go:45` enforces `tokenPolicyMinIdleRevokeDays = 7` — the backend rejects any `idle_revoke_days` < 7 with `codes.InvalidArgument` (surfaced as 400 via `mapTokenPolicyGRPCError`). `frontend/src/components/access/PoliciesPanel.tsx:114 validateSection` only enforces the outer `MIN_DAYS = 1` / `MAX_DAYS = 3650` bounds, applied uniformly to all three dimensions. Consequence: a workspace admin who sets `idle_revoke_days = 3` clicks Save, hits a raw BE error banner, and has to guess-and-retry. The PR message correctly says "Client-side validation rejects <=0 or non-integer values BEFORE calling the mutation" — but the tighter per-field floor (7 days for idle-revoke) is not surfaced. No security exposure — the BE catches this — but the plan explicitly promised BE + FE bounds parity ("Bounds validation — 0 or negative days rejected at both BE service layer AND FE client layer") and this dimension's floor is missing.
- **Remediation:**
  1. In `PoliciesPanel.tsx`, thread a per-dimension `minDays` prop into `PolicySection` + `validateSection`. Set to `7` for `idle_revoke_days`, `1` for the other two.
  2. Add a `PoliciesPanel.test.tsx` case that sets idle-revoke to 3, clicks Save, and asserts the inline validation banner reads "Idle revoke: Value must be at least 7" without a network call.
- **References:** CLAUDE.md §7 (Input Validation — allowlist), plan §"Bounds validation" gate.

### SEC-066 — `PutTokenPolicy` gRPC surface trusts caller-supplied `tenant_id` without cross-check
- **Severity:** MEDIUM
- **Status:** RESOLVED — WON'T FIX (not applicable in single-tenant posture)
- **Service:** `services/auth`
- **Raised:** 2026-07-01 (pre-PR review of `feat/fut-003-token-policies`)
- **Resolved:** 2026-07-02 — closed as **not applicable** to the platform's supported posture. SEC-066 is a multi-tenant-only finding: the exploit is "a direct-gRPC caller forges `tenant_id` to mutate ANOTHER tenant's policy row / trigger a cross-tenant mass-revoke." Under REDESIGN-001 the platform ships single-tenant (`DEPLOYMENT_MODE=single`, the default and supported posture) — there is no second tenant to attack, all API keys/users belong to the one bootstrap tenant, and a policy row written for a bogus `tenant_id` governs zero real keys. The auth gRPC server additionally wires `SingleTenantInjector` (`server.go:117`) in single mode. The proposed "shared `PeerTenantCheck`/`PeerActorCheck` interceptor" durable fix is therefore **withdrawn** — it would defend a threat that cannot occur in the supported deployment. **For `multi`-mode operators** (the preserved-but-secondary posture) the standing mitigation is unchanged and documented: set `MTLS_PEER_CN_ALLOWLIST=registry-management` on `services/auth` so only the BFF — which sources `tenant_id`/`actor_id` from the verified JWT — can reach these admin RPCs. The residual `actor_id`-audit-integrity concern inherited from SEC-069 is likewise a compromised-internal-peer threat gated by that same allowlist, not a new interceptor.
- **Description:** `services/auth/internal/handler/grpc_token_policy.go:58 PutTokenPolicy` takes `tenant_id` verbatim from `req.GetTenantId()` and never cross-checks it against gRPC metadata / the calling mTLS peer's tenant claim. The docstring at `grpc_token_policy.go:9` explicitly says "the gRPC layer trusts its caller — RBAC gates land in services/management's BFF." In production (single mode) the `SingleTenantInjector` clamps every incoming tenant id to `bootstrap_tenant_id` and mismatches return `codes.InvalidArgument` — SO **single mode is safe**. In multi mode (deployments still using `DEPLOYMENT_MODE=multi`) any client with a valid mTLS cert (e.g. a compromised sibling service) can call `PutTokenPolicy(tenant_id="<any-other-tenant>")` and mutate that tenant's policy row — including forcing `idle_revoke_days=7` to trigger a mass revoke on the next worker tick. The BFF's admin-gate at `access_token_policy.go:100 h.isTenantAdminOrPlatformAdmin(r)` correctly sources `tenantID` from the JWT, so the BFF path is safe. The exposure is limited to a direct-gRPC caller in multi mode + `MTLS_PEER_CN_ALLOWLIST` disabled or including a permissive CN. Compare to the sibling `GetTokenPolicy` — same shape, same trust boundary. This mirrors the ambient posture of every other auth gRPC RPC (OIDC trust, etc.) so it's not a regression introduced by FUT-003, but the surface *is* new.
- **Remediation:**
  1. Short term (multi mode): document that operators MUST populate `MTLS_PEER_CN_ALLOWLIST` on `services/auth` so only `registry-management` can call `PutTokenPolicy`. Bump `registry_grpc_peer_cn_allowlist_enabled` alerting to page when the allowlist is empty in prod.
  2. Medium term: add a `libs/middleware/grpc.PeerTenantCheck` interceptor that reads a tenant id from mTLS SAN or gRPC metadata and rejects any RPC whose request-body `tenant_id` disagrees. Wire it on every `services/auth` RPC that takes a tenant id in its request.
  3. In-scope for this PR (optional): mirror `SingleTenantInjector` behaviour by adding an explicit `tenant_id` cross-check in `PutTokenPolicy` when the caller supplies gRPC metadata `x-tenant-id`.
- **References:** CLAUDE.md §7 (Auth token flow — tenant cross-check), CLAUDE.md §12 (gRPC interceptors — tenant ID extraction), CWE-639 (Authorization Bypass Through User-Controlled Key).

### SEC-067 — `Upsert(all-nil)` rewrites `updated_by_user_id` on a no-op call, muddling audit trail
- **Severity:** LOW
- **Status:** RESOLVED
- **Resolved:** 2026-07-01 — PR #226. Remediation option 2: the audit emit is skipped when the before + after policy snapshots are byte-identical, so a no-op `{}` PUT no longer fires an `auth.token_policy.changed` event. (The DB `updated_by_user_id` stamp on a no-op remains — accepted, the audit trail is the tamper-evident record.)
- **Service:** `services/auth`
- **Raised:** 2026-07-01 (pre-PR review of `feat/fut-003-token-policies`)
- **Description:** `services/auth/internal/repository/token_policy.go:83 Upsert` documents "A caller that Puts an all-nil policy is semantically saying 'no change, but stamp me as the last toucher'." The COALESCE-based update preserves the three limit fields but always writes `updated_by_user_id = EXCLUDED.updated_by_user_id`. Combined with `TokenPolicyService.Put` at `token_policy.go:105` which does NOT reject an all-nil request (only `TenantID` and `ActorID` are required), any workspace admin can call `PUT /api/v1/access/token-policy` with an empty JSON `{}` body and (a) trigger an `auth.token_policy.changed` audit event with an empty before/after diff and (b) rewrite `updated_by_user_id` to their own id — silently taking credit for the previous admin's configuration. An attacker who has compromised one admin account can use this to launder their identity into the audit trail as the "last admin who touched the policy." Not exploitable to gain new privilege but pollutes the audit trail with noise + credit-laundering.
- **Remediation:**
  1. In `TokenPolicyService.Put` (`services/auth/internal/service/token_policy.go:105`), reject all-nil input with `codes.InvalidArgument("at least one limit field must be provided")` before consulting the repo.
  2. Alternatively (fewer breakage risks): in the audit emit path, short-circuit `emitPolicyChanged` when `before` and `after` snapshots are identical (`reflect.DeepEqual`) so a no-op update doesn't fire an event. Keeps the DB idempotent but preserves the audit-noise fix.
- **References:** CLAUDE.md §10 (Audit — no tampering / no credit laundering), OWASP ASVS V7.3 (Audit log completeness), CWE-1288 (Improper Validation of Consistency within Input).

### SEC-068 — `SnoozeAPIKeyReview` BFF admin path skips tenant scoping — cross-tenant snooze + audit-trail forgery (multi-mode)
- **Severity:** HIGH (multi mode) / N/A (single mode — no second tenant exists)
- **Status:** RESOLVED
- **Service:** `services/management`
- **Raised:** 2026-07-01 (pre-PR review of `feat/fut-004-access-review`, code already merged as PR #227)
- **Resolved:** 2026-07-01 — PR #228 shipped remediation #1: the tenant-scoped `ListStaleKeys` pre-flight now runs for EVERY caller (admin included), so an unknown/foreign key id returns 404 regardless of role; regression test `TestSnoozeAPIKeyReview_admin_unknownKey_returns404`. 2026-07-02 — `fix/sec-068-access-review-tenant-scoping` shipped remediation #2 (defence-in-depth): `tenant_id` added to the proto (SEC-069) and cross-checked in `AccessReviewService.SnoozeAPIKeyReview`, so a future BFF regression can no longer reach the DB write cross-tenant.
- **Description:** `services/management/internal/handler/access_review.go:141 handleSnoozeAPIKeyReview` gates ownership resolution behind `if !isAdmin`. When `isTenantAdminOrPlatformAdmin(r) == true` the handler passes `body.KeyID` straight through to `authv1.SnoozeAPIKeyReviewRequest.KeyId` without any check that the key belongs to the caller's tenant. **Single-mode note:** in `DEPLOYMENT_MODE=single` there is by construction no second tenant to attack — this finding is only exploitable in `DEPLOYMENT_MODE=multi`. Since the default posture (CLAUDE.md §1) is `single`, real-world exposure across OSS deployments is expected to be near-zero; the finding still stands because (a) multi-mode operators exist and are the more likely target of tenant-admin abuse, and (b) the fix is small enough to land in the same follow-up as SEC-069. Downstream, `services/auth/internal/service/access_review.go:265 SnoozeAPIKeyReview` calls `repo.GetTenantIDForKey(ctx, in.KeyID)` which returns the key's own `tenant_id` without filtering. Result: a workspace admin of tenant A supplies a UUID that belongs to tenant B (guessed / phished / obtained from a shared audit log / a compromised-CI test artefact) and the auth service snoozes tenant B's key, emits `RoutingAccessReviewSnoozed` with `TenantID = <tenant B>` and `ActorID = <caller from tenant A>`. Two concrete impacts: (a) tenant B's operator loses their weekly nudge on that key without any indication, defeating the entire purpose of FUT-004's nudge-only surface; (b) tenant B's audit trail records an actor id from tenant A, which their FE will render as a raw UUID and their /activity view will not be able to attribute — breaking the audit invariant that every event has an in-tenant principal. Compounded by SEC-069 (gRPC surface has no `tenant_id` field, so tenant scoping is not even possible without a code change). Note: `isTenantAdminOrPlatformAdmin` returns true for any tenant admin scoped to the current JWT tenant — this is NOT a platform-admin-only bug, every workspace admin in the system can attack every other workspace.
- **Remediation:**
  1. In the admin branch of `handleSnoozeAPIKeyReview` (before line 196), call `h.auth.ListStaleKeys(r.Context(), &authv1.ListStaleKeysRequest{TenantId: tenantID})` and require `ownerOfKey(listResp.GetKeys(), body.KeyID)` to return `found == true` — same pre-flight the non-admin path already runs. Cost: one extra gRPC on the admin path (already paid on the non-admin path).
  2. Preferred / defence-in-depth: also add `tenant_id` to `SnoozeAPIKeyReviewRequest` (see SEC-069) and cross-check inside `AccessReviewService.SnoozeAPIKeyReview` — `if got_tenant != in.TenantID { return codes.NotFound }`. Mirrors the shape of the fix proposed for SEC-066.
  3. Add a regression test at `services/management/internal/handler/access_review_test.go` that seeds two tenants + admin user in tenant A + key in tenant B, calls POST /snooze with tenant B's key id, and asserts 404 (not 200).
- **References:** CLAUDE.md §7 (Auth token flow — tenant cross-check), CLAUDE.md §9 (Multi-Tenancy — never query across tenants), CWE-639 (Authorization Bypass Through User-Controlled Key), CWE-863 (Incorrect Authorization).

### SEC-069 — `SnoozeAPIKeyReviewRequest` gRPC surface has no `tenant_id` + trusts caller-supplied `actor_id`
- **Severity:** MEDIUM
- **Status:** RESOLVED (tenant scoping) / residual `actor_id` concern folded into SEC-066 remediation #2
- **Service:** `services/auth`
- **Raised:** 2026-07-01 (pre-PR review of `feat/fut-004-access-review`, code already merged as PR #227)
- **Resolved:** 2026-07-02 — `fix/sec-068-access-review-tenant-scoping`. Remediations #1 + #2 shipped: `string tenant_id = 4` added to `SnoozeAPIKeyReviewRequest`; the BFF populates it from `middleware.TenantIDFromContext` (JWT claims); `AccessReviewService.SnoozeAPIKeyReview` cross-checks it against the key's own tenant and returns opaque `codes.NotFound` on mismatch. Empty `tenant_id` skips the check (rolling-deploy tolerance) — the BFF test fake rejects an empty value so the plumbing can't silently regress. Regression tests: `TestAccessReviewService_SnoozeAPIKeyReview_TenantMismatchReturnsNotFound` (+ matching-tenant happy path). **Residual:** the `actor_id` spoof-by-direct-gRPC-caller concern (remediation #4) is the same class as SEC-066 and is tracked there — the shared `PeerActorCheck`/`PeerTenantCheck` interceptor is the durable fix; until then remediation #3 stands (operators MUST set `MTLS_PEER_CN_ALLOWLIST=registry-management` on `services/auth` in production).
- **Description:** `proto/auth/v1/auth.proto:528 SnoozeAPIKeyReviewRequest` declares only `key_id`, `days`, `actor_id`. No `tenant_id`. The gRPC handler at `services/auth/internal/handler/grpc_access_review.go:65 SnoozeAPIKeyReview` never checks tenant provenance — the docstring at :11 says "the gRPC layer trusts its caller — RBAC / owner-vs-admin gates land in services/management's BFF." Two related risks: (a) tenant scoping is IMPOSSIBLE at the gRPC layer without a proto change, so the BFF must carry 100% of the tenant-check burden and any BFF regression (see SEC-068) becomes a full cross-tenant bypass; (b) `actor_id` is a raw string on the wire — a direct-gRPC caller with a valid mTLS cert can pass `actor_id = "<any-uuid>"` and stamp the audit event with a forged actor. Same class as SEC-066 (`PutTokenPolicy` trusts caller-supplied `tenant_id`) but a step worse because there is no field to cross-check against at all. In single mode the `SingleTenantInjector` clamps every incoming request to `bootstrap_tenant_id` and rejects mismatches — SO **single mode is safe from tenant-cross** but NOT from actor-id spoof. In multi mode with a permissive `MTLS_PEER_CN_ALLOWLIST` (or none), both paths are open. Same shape also applies to `ListStaleKeysRequest`: it does take `tenant_id`, but there's no cross-check that the gRPC peer's identity matches.
- **Remediation:**
  1. Add `string tenant_id = 4;` to `SnoozeAPIKeyReviewRequest` in the proto. Populate from `middleware.TenantIDFromContext(r.Context())` in the BFF.
  2. In `AccessReviewService.SnoozeAPIKeyReview` cross-check the derived-from-row tenant against the request-supplied tenant; `codes.NotFound` on mismatch (opaque failure, no leak of existence).
  3. Enforce that operators MUST populate `MTLS_PEER_CN_ALLOWLIST=registry-management` on `services/auth` in production so only the BFF can call these RPCs. Bump the deployment runbook.
  4. For `actor_id`: same class as SEC-066 — add a `libs/middleware/grpc.PeerActorCheck` interceptor that reads the actor id from mTLS SAN (or a trusted gRPC metadata header signed by the BFF's mTLS cert) and rejects any RPC whose request-body `actor_id` disagrees. Bulk fix across every FUT-001..004 RPC.
- **References:** CLAUDE.md §7 (Auth token flow — tenant cross-check + actor plumbed from JWT sub), CLAUDE.md §12 (gRPC interceptors — tenant ID extraction), CWE-639 (Authorization Bypass Through User-Controlled Key), CWE-345 (Insufficient Verification of Data Authenticity).

### SEC-070 — Audit consumer swallows `json.Unmarshal` errors for FUT-004 payloads
- **Severity:** LOW
- **Status:** RESOLVED (FUT-004 cases) / consumer-wide sweep tracked as follow-up
- **Service:** `services/audit`
- **Raised:** 2026-07-01 (pre-PR review of `feat/fut-004-access-review`, code already merged as PR #227)
- **Resolved:** 2026-07-02 — `fix/sec-068-access-review-tenant-scoping`. Both FUT-004 `mapEvent` cases (`access_review.due`, `access_review.snoozed`) now log + drop malformed payloads (nil → ACK, no blank-Resource insert). Test `TestMapEvent_accessReview_malformedPayloadDropped` covers both routing keys + the well-formed happy path. **Residual:** the same `_ = json.Unmarshal` pattern remains in the ~25 pre-existing `mapEvent` cases — roll the fix consumer-wide alongside the FUT-048 consumer-hardening batch (futures.md), plus the optional `event.Version` gate (remediation #3).
- **Description:** `services/audit/internal/eventconsumer/consumer.go:986` and `:1004` both use `_ = json.Unmarshal(event.Payload, &p)` for `AccessReviewDuePayload` and `AccessReviewSnoozedPayload`. A malformed payload (e.g. a broker replay of an older/newer event shape, or a hostile publisher on the same exchange) silently produces a zero-value `p` — the audit row is still inserted with `Resource = ""` (empty KeyID), `ActorID = "system"` fallback for the snoozed case. This is consistent with the pre-existing pattern used by every prior payload in `mapEvent` (RoutingPushImage, RoutingScanCompleted, etc.), so it's NOT a regression introduced by FUT-004. But it means audit rows can be silently corrupted by a malformed payload rather than being NACK+DLQ'd or logged as a parse error. Because `services/auth` is the sole publisher of both routing keys and both are typed structs on the producer side, real-world exposure is near-zero unless: (a) the exchange is ever exposed to a hostile publisher, or (b) a future producer-side refactor changes the payload shape without a version bump.
- **Remediation:**
  1. Handle the unmarshal error: on failure, log via `slog.WarnContext(ctx, "audit: malformed FUT-004 payload", "action", event.Type, "err", err)` and return `nil` (skip the audit insert entirely, so we don't stamp a blank-Resource row into the table).
  2. Roll the same fix across all case-branches in `mapEvent` as a follow-up hardening pass — the pattern is genuinely wrong everywhere, but shouldn't block this PR.
  3. Consider adding an `event.Version` gate in `mapEvent` so a producer-side shape change with a bumped version cleanly fails-closed until the consumer catches up.
- **References:** CLAUDE.md §10 (Audit — no tampering, no silent corruption), CWE-20 (Improper Input Validation).

---

## Pentest Findings — 2026-06-18

> Findings from a thorough security review of the system. Each item is logged
> with a reproducible description and a concrete remediation path so they can
> be triaged into a fix sprint. ID prefix `PENTEST-` keeps these separate from
> the original SEC- items (which were author-flagged during development).
>
> **Triage tip:** CRITICAL and HIGH should be fixed before any non-local
> deployment. MEDIUM should be fixed before GA. LOW and INFO can be batched.

### CRITICAL

| ID | Severity | Title | Service | Status |
|---|---|---|---|---|
| PENTEST-001 | CRITICAL | Audit HTTP API unauthenticated | `registry-audit` | RESOLVED ✅ (2026-06-18) |

**PENTEST-001 — Audit HTTP API has no authentication** — RESOLVED ✅
- **Original issue:** `services/audit/internal/server/server.go:100-101` registered `POST /audit/events` and `GET /audit/events` with no auth middleware. Any process that could reach the audit pod's HTTP port (8080 by default) could forge audit log entries for any tenant, read every tenant's audit trail, or DoS the audit pipeline.
- **Resolution (2026-06-18):** Applied remediation option (c) — **removed the HTTP write/query API entirely**. Verified via grep that no caller anywhere in the codebase consumed `POST/GET /audit/events`; the endpoints were dead code. All audit writes already flow through the RabbitMQ `eventconsumer` (durable + DLQ via `audit.events` queue with routing key `#`), and reads flow through the mTLS-gated `AuditService` gRPC API consumed by `registry-management.GetBuildHistory`. The fix:
  1. Removed route registrations from `services/audit/internal/server/server.go`
  2. Deleted `services/audit/internal/handler/http.go` (the unused `HTTPHandler`, `WriteEvent`, `QueryEvents`)
  3. Retained `/healthz` on the HTTP port for liveness probes
  4. Added a comment block in `server.go` documenting that re-introducing an HTTP write/query API requires mTLS + CN allowlist
- **Defense-in-depth result:** Audit log integrity now depends only on (1) mTLS on the gRPC port + (2) FORCE RLS + `registry_audit_app` low-privilege role (SEC-001) + (3) RabbitMQ DLQ for malformed events. No HTTP attack surface.

---

### HIGH

| ID | Severity | Title | Service | Status |
|---|---|---|---|---|
| PENTEST-002 | HIGH | RBAC scope not enforced in management API | `registry-management` | RESOLVED ✅ (2026-06-18) |
| PENTEST-003 | HIGH | Public user creation with arbitrary tenant_id | `registry-auth` | RESOLVED ✅ (2026-06-18) |
| PENTEST-004 | HIGH | Username enumeration via login timing attack | `registry-auth` | RESOLVED ✅ (2026-06-18) |
| PENTEST-005 | HIGH | Username enumeration via lockout/disabled status codes | `registry-auth` | RESOLVED ✅ (2026-06-18) |

**PENTEST-002 — RBAC scope not enforced in management API** — RESOLVED ✅
- **Original issue:** `getUserRoles` returned a flat list of role names for the entire tenant. All RBAC enforcement sites used `hasRole(roles, "admin")` without scope, so admin-of-org-A could grant/revoke roles in org-B, delete repos in org-B, etc.
- **Resolution (2026-06-18):** Introduced a scope-aware authorization model end-to-end:
  1. **Proto:** added `repeated RoleAssignment role_assignments = 3` to `GetUserPermissionsResponse` so callers receive full per-scope assignment info (not just role names).
  2. **Auth backend:** `services/auth/internal/handler/grpc.go` `GetUserPermissions` now populates the new field with `{role, scope_type, scope_value, id}` for every assignment.
  3. **Management:** added `getUserAssignments(r)` and `hasScopedRole(assignments, scopeType, scopeValue, minRole)` in `services/management/internal/handler/rbac.go`. The helper implements the containment rule: org-scoped grants cover all repos in that org (`"myorg/anything"` matches an org grant on `"myorg"`); repo grants do NOT bubble up to the parent org or sibling repos.
  4. **Updated every enforcement site** to call `hasScopedRole` with the URL's actual scope:
     - `handleCreateRepository` → `("org", body.Org, "admin")`
     - `handleDeleteRepository` → `("repo", "org/repo", "admin")`
     - `handleDeleteTag` → `("repo", "org/repo", "writer")`
     - `handleTriggerScan` → `("repo", "org/repo", "writer")` (was previously unchecked — bonus fix)
     - `handleGrantOrgMember` / `handleRevokeOrgMember` → `("org", org, "admin")`
     - `handleGrantRepoMember` / `handleRevokeRepoMember` → `("repo", "org/repo", "admin")`
  5. **Tests:** `services/management/internal/handler/rbac_test.go` adds 6 dedicated tests including the specific attack scenarios — `orgGrantDoesNotCoverSiblingOrg`, `repoGrantDoesNotCoverSiblingRepo`, and `orgPrefixIsNotSubstring` (a "my" admin must not match "myorg/...").
- **Cross-check:** the auth-side `GrantRole`/`RevokeRole` gRPC handlers still don't authorize the caller (they only insert/delete). This is acceptable because gRPC is mTLS-restricted to internal services that perform authz before calling — but if any new service ever calls these handlers, it must enforce scope-aware authz on its own caller too. Future hardening: add caller authz inside the auth gRPC handlers as defence-in-depth.

**PENTEST-003 — Public user creation with arbitrary tenant_id** — RESOLVED ✅
- **Original issue:** `POST /api/v1/users` was unauthenticated and accepted any `tenant_id` from the request body. Allowed account squatting, username enumeration via 409 responses, user-table DoS via Argon2 spam, and cross-tenant user injection (attacker logs in as the injected user and gets a JWT carrying the target tenant's UUID).
- **Resolution (2026-06-18):** Applied remediation option (a) — admin-only endpoint:
  1. `createUser` now calls `requireAuth` first; anonymous requests get `401 UNAUTHORIZED`.
  2. The target tenant is taken from the caller's JWT `tenant_id` claim. If `body.tenant_id` is supplied it must match — otherwise `403 DENIED "cannot create users in a different tenant"`.
  3. The caller must hold an `admin` or `owner` role somewhere in that tenant, verified via a new `callerIsTenantAdmin` helper that calls `svc.GetUserRoles` and fails closed on lookup error. Non-admins get `403 DENIED "admin role required to create users"`.
  4. Bootstrap (first user in a tenant) deliberately CANNOT happen through this endpoint — it must come from a seed migration (`services/auth/migrations/20260610000001_seed_dev_tenant.sql` does this for dev) or out-of-band tooling. This is by design: an unauthenticated bootstrap path would re-introduce the original vulnerability.
- **Tests:** `services/auth/internal/handler/http_test.go` adds three dedicated security tests — `TestCreateUser_noAuth_returns401`, `TestCreateUser_callerNotAdmin_returns403`, `TestCreateUser_crossTenant_returns403` — plus updates the existing happy-path tests to thread an admin token through the new `newAdminAuthedRequest` helper. All 7 createUser tests pass.
- **Follow-up considerations:** A future "platform admin" endpoint for super-admins who manage multiple tenants would need a separate route (e.g. `POST /api/v1/admin/tenants/{id}/users`) gated by a new platform-admin role marker — out of scope for this fix.

**PENTEST-004 — Username enumeration via login timing attack** — RESOLVED ✅
- **Original issue:** Unknown user → fast path (~5 ms, DB lookup only). Known user, wrong password → slow path (~100 ms, Argon2id verify). The reliable measurable gap let an attacker enumerate valid usernames over the network.
- **Resolution (2026-06-18):** Added `dummyArgonHash()` in `services/auth/internal/service/auth.go` — a lazily-generated (`sync.Once`) Argon2id hash of a throwaway password. In `AuthenticateUser`, when `GetByUsername` returns `ErrNotFound`, we still call `argon2pkg.Verify(password, dummyArgonHash())` and discard the result, so the wall-clock time matches the known-user-wrong-password path.
- **Tests:** `TestAuthenticateUser_unknownUsername_runsDummyVerify` directly measures both paths and fails if the ratio diverges by more than 4× — a deliberately loose threshold (CI flakiness) but tight enough to catch a regression that bypasses the dummy verify (would yield a >10× gap).

**PENTEST-005 — Username enumeration via lockout/disabled status codes** — RESOLVED ✅
- **Original issue:** `403 "account locked"` and `403 "account disabled"` leaked whether a username existed in the tenant.
- **Resolution (2026-06-18):** Both HTTP handlers (`/auth/token` and `/api/v1/login`) now collapse ALL auth-failure variants — unknown user, wrong password, locked, disabled — to one identical `401 UNAUTHORIZED "invalid credentials"` response. A new `logAuthFailure` helper classifies the underlying cause at `slog.Info`/`slog.Warn` server-side so ops still see lockout events. The typed errors (`ErrAccountLocked`, `ErrAccountDisabled`) remain in the service layer for internal flow control but never propagate to the wire.
- **Tests:** `TestLogin_unknownVsKnown_returnsSameStatusAndBody` asserts that probing an unknown username and a known username with the wrong password produces identical HTTP responses (same status, byte-identical body) — the explicit no-oracle guarantee. The three legacy tests that asserted the old `403 "account locked/disabled"` behavior were inverted to assert `401` (renamed `_returns401_noLeakage`).

---

### MEDIUM

| ID | Severity | Title | Service | Status |
|---|---|---|---|---|
| PENTEST-006 | MEDIUM | Member list endpoints leak roles to non-admin tenant users | `registry-management` | RESOLVED ✅ (2026-06-18) |
| PENTEST-007 | MEDIUM | Webhook response body not size-limited | `registry-webhook` | RESOLVED ✅ (2026-06-18) |
| PENTEST-008 | MEDIUM | CORS middleware always returns allowed origin | `registry-management` | RESOLVED ✅ (2026-06-18) |
| PENTEST-009 | MEDIUM | WWW-Authenticate parser splits on comma naively | `registry-proxy` | RESOLVED ✅ (2026-06-18) |
| PENTEST-010 | MEDIUM | AUTH_REALM default uses HTTP, not HTTPS | `registry-core`, `registry-proxy` | RESOLVED ✅ (2026-06-18) |
| PENTEST-011 | MEDIUM | RBAC revoke does not verify assignment belongs to scope | `registry-management` | RESOLVED ✅ (2026-06-18) |

**PENTEST-006 — Member list leaks roles** — RESOLVED ✅
- **Original issue:** `handleListOrgMembers` and `handleListRepoMembers` had no role check; any authenticated tenant user could enumerate org/repo members.
- **Resolution (2026-06-18):** Both handlers now require at least `reader` on the target scope via `hasScopedRole`. Non-members receive `404 not found` (not `403 forbidden`) so the existence of the org/repo isn't confirmed. Bundled with the PENTEST-002 fix.

**PENTEST-007 — Webhook response body not size-limited** — RESOLVED ✅
- **Original issue:** `services/webhook/internal/delivery/dispatcher.go` drained the full response body with `io.Copy(io.Discard, resp.Body)` — no upper bound. A hostile webhook endpoint could stream unbounded bytes back, tying up worker goroutines for the full request timeout.
- **Resolution (2026-06-18):** Added `maxResponseBytes = 8 * 1024` constant and wrapped the discard copy with `io.LimitReader(resp.Body, maxResponseBytes)`. Webhook ACKs are typically empty or a few hundred bytes; 8 KB is generous. Same hardening applied opportunistically to the signer Vault key-fetch and sign paths (both now capped at 64 KB) — they previously read unbounded `io.ReadAll` on the error path.

**PENTEST-008 — CORS middleware always returns configured origin** — RESOLVED ✅
- **Original issue:** The middleware unconditionally echoed a fixed origin and never set `Vary: Origin`, weakening defense-in-depth and blocking any future multi-origin support.
- **Resolution (2026-06-18):** Rewrote `services/management/internal/middleware/cors.go` to:
  - Accept a comma-separated allowlist (`CORS_ALLOWED_ORIGIN=https://a.example,https://b.example`) — single-origin configurations still work since they're a one-element list.
  - Always emit `Vary: Origin` so caching proxies key on origin even for blocked responses.
  - Echo `Access-Control-Allow-Origin` only when the request's `Origin` is in the allowlist (exact RFC 6454 match, case-sensitive). Disallowed origins receive no CORS headers and the browser blocks via SOP.
  - Skip CORS headers entirely on non-CORS requests (no `Origin` header) so same-origin responses stay clean.
  - Always return 204 for OPTIONS, regardless of allowlist outcome, so an attacker can't probe the allowlist via preflight differences.
- **Tests:** 5 new tests in `cors_test.go`: allowed-origin echo, disallowed-origin omission (the defining PENTEST-008 test), no-Origin clean response, preflight-always-204, and case-sensitive matching.

**PENTEST-009 — `parseBearerChallenge` splits on `,` naively** — RESOLVED ✅
- **Original issue:** The parser used `strings.Split(header, ",")` which broke for quoted values containing commas (e.g. `scope="repository:foo,bar:pull"`), causing pull failures against any upstream registry that uses comma-bearing scopes.
- **Resolution (2026-06-18):** Rewrote `parseBearerChallenge` with a quote-aware tokenizer (`splitCommaRespectingQuotes`) that walks the header tracking quote state, plus an `unescapeQuoted` helper that resolves the RFC 7230 backslash escapes (`\"` → `"`, `\\` → `\`) inside quoted strings. Comma-bearing scopes are now preserved as a single value.
- **Tests:** 4 new tests in `parse_challenge_test.go`: simple Docker Hub-style header, the defining quoted-comma case, escaped quotes inside a value, and tolerance of malformed segments (extra whitespace, missing `=`).

**PENTEST-010 — AUTH_REALM defaults to HTTP** — RESOLVED ✅
- **Original issue:** Both `registry-core` and `registry-proxy` defaulted `AUTH_REALM` to `http://localhost:8080/auth/token`. An operator deploying without overriding it would direct Docker clients to send Basic-auth credentials over plaintext.
- **Resolution (2026-06-18):** Added `validateAuthRealm(realm, environment)` in both `services/core/internal/config/config.go` and `services/proxy/internal/config/config.go`. The validator:
  - **Refuses** `http://` when `OTEL_ENVIRONMENT` is `production` or `staging` — startup fails fast with a clear error.
  - **Warns** at `slog.Warn` when `http://` is used in any other environment (development, empty, etc.) so misconfiguration is visible in logs.
  - **Accepts** `https://` everywhere; rejects other schemes (`ftp://` etc.) outright.
  - Scheme matching is case-insensitive (`HTTPS://` accepted) but the rest of the URL is preserved verbatim.
- **Tests:** 10 table-driven subtests in `auth_realm_test.go` covering every combination of scheme × environment plus malformed-URL / case-folding paths. Both core and proxy share the same validator semantics so the test coverage applies to both.

**PENTEST-011 — Revoke does not verify assignment belongs to scope** — RESOLVED ✅
- **Original issue:** Both revoke handlers passed the assignment ID to the auth gRPC `RevokeRole` without verifying the assignment's scope matched the URL path. Admin-of-org-A could delete assignments in org-B by URL-guessing or by visibility through `ListMembers`.
- **Resolution (2026-06-18):** Added two new fields to `RevokeRoleRequest` proto — `expected_scope_type` and `expected_scope_value`. Management's revoke handlers populate them from the URL path. Auth's `RevokeRoleScoped` repository method extends the DELETE SQL with `($3 = '' OR scope_type = $3) AND ($4 = '' OR scope_value = $4)` so a mismatched assignment matches zero rows and returns `ErrNotFound`. Auth's gRPC handler maps that to `codes.NotFound` indistinguishable from "row doesn't exist" — preventing scope enumeration via error differences.

---

### LOW

| ID | Severity | Title | Service | Status |
|---|---|---|---|---|
| PENTEST-012 | LOW | TLS minimum version is 1.2 | `libs/auth/mtls` | RESOLVED ✅ (2026-06-18) |
| PENTEST-013 | LOW | Authorization header parsing is case-sensitive | `registry-management`, `registry-core` | RESOLVED ✅ (2026-06-18) |
| PENTEST-014 | LOW | No per-tenant read rate limit on management API | `registry-management` | RESOLVED ✅ (2026-06-18) |
| PENTEST-015 | LOW | Dashboard `useUserIsAdmin` reads from non-existent localStorage entry | `frontend/` | RESOLVED ✅ (2026-06-18) |
| PENTEST-016 | LOW | Audit HTTP `QueryEvents` allows unbounded `limit` param | `registry-audit` | RESOLVED ✅ (2026-06-18) |

**PENTEST-012 — TLS 1.2 minimum** — RESOLVED ✅
- **Original issue:** `libs/auth/mtls/mtls.go` set `MinVersion: tls.VersionTLS12` for both server and client mTLS configs. Internal service-to-service mTLS has no legacy-client constraint and should mandate the modern baseline.
- **Resolution (2026-06-18):** Set `MinVersion: tls.VersionTLS13` in both `ServerTLSConfig` and `ClientTLSConfig`. TLS 1.3 mandates forward secrecy and AEAD-only cipher suites, removing legacy renegotiation. No external clients touch these gRPC ports — all calls are service-to-service inside the cluster.

**PENTEST-013 — Authorization header case-sensitive parse** — RESOLVED ✅
- **Original issue:** Hand-rolled `strings.HasPrefix(authHeader, "Bearer ")` checks rejected `bearer xyz` (lowercase) and other case variants even though RFC 7235 §2.1 makes auth scheme names case-insensitive.
- **Resolution (2026-06-18):** Created `libs/auth/bearer/bearer.go` with `Extract(authHeader)` that case-insensitively matches the `Bearer` scheme and returns the token plus a found-flag. Updated every parsing site (`registry-auth` `requireAuth` + `refreshToken`, `registry-core` `authenticate`, `registry-proxy` `authenticate`, `registry-management` `RequireAuth`) to use the helper. Basic-auth parsing in core/proxy also switched to `strings.EqualFold` for symmetry.
- **Tests:** 12 table-driven cases in `bearer_test.go` covering all-uppercase, all-lowercase, mixed-case, tab separator, scheme-only, empty, Basic-scheme rejection, and the `BearerExt`-confusable rejection.

**PENTEST-014 — No per-tenant read rate limit** — RESOLVED ✅
- **Original issue:** No per-user cap on `/api/v1/*` reads. An authenticated tenant user could hammer stats/repositories endpoints to drive load on metadata + audit.
- **Resolution (2026-06-18):** Added `PerUserRateLimiter` in `services/management/internal/middleware/ratelimit.go`:
  - In-process token bucket via `golang.org/x/time/rate`, keyed by user_id from `UserIDFromContext`.
  - Default 20 rps with burst 40 — generous for an interactive dashboard, blocks a runaway script.
  - Background GC sweeps stale buckets every 5 minutes (10-minute idle TTL), keeping memory bounded.
  - Returns `429 Too Many Requests` with `Retry-After: 1` when exceeded.
  - Passes through requests without an authenticated user_id (e.g. `/healthz`) so unauthenticated probes don't poison everyone's bucket.
  - Wired into `Handler.Register` after `RequireAuth` populates context, so the limiter sees the user_id. Optional via `WithRateLimiter` for tests that need deterministic timing.
- **Multi-replica note:** in-process by design; with N replicas the effective cluster cap is N×20 rps. A Redis-backed limiter can drop in transparently if a global cap is needed, by satisfying the same `Middleware` signature.

**PENTEST-015 — `useUserIsAdmin` reads non-existent localStorage entry** — RESOLVED ✅
- **Original issue:** `dashboard/index.tsx:22` read `localStorage.getItem('auth_token')` — a key that's never written anywhere (the token lives only in Zustand memory per FE-SEC-001). The hook always returned `false`, so admin UI was permanently hidden.
- **Resolution (2026-06-18):**
  1. Added `roles?: string[]` to `AuthUser` in `frontend/src/store/authStore.ts` so the existing `JSON.parse(atob(...)) as AuthUser` decode path picks up the JWT `roles` claim end-to-end (backend already emits it per the PENTEST-002 / roles-claim work).
  2. Rewrote `useUserIsAdmin` in `frontend/src/routes/_authenticated/dashboard/index.tsx` to read `roles` from `useAuthStore` and check `includes('admin') || includes('owner')`.
- **Verified:** frontend `tsc --noEmit` clean. End-to-end chain: backend `Login` → JWT roles claim → Zustand store → admin UI gate.

**PENTEST-016 — Audit `limit` param** — RESOLVED ✅ (by removal)
- **Resolution (2026-06-18):** The entire audit HTTP API (`POST/GET /audit/events`) was removed in the PENTEST-001 fix. The `limit` query parameter no longer exists. The audit query path is now gRPC-only (`AuditService.GetBuildHistory`), which enforces its own server-side cap.

---

### INFORMATIONAL

| ID | Severity | Title | Service | Status |
|---|---|---|---|---|
| PENTEST-017 | INFO | Default dev credentials in docker-compose | `infra/` | RESOLVED ✅ (2026-06-18) |
| PENTEST-018 | INFO | `sslmode=prefer` in dev compose | `infra/` | RESOLVED ✅ (2026-06-18) |
| PENTEST-019 | INFO | Scanner plugin cache directory writable by non-root | `registry-scanner` | RESOLVED ✅ (2026-06-18) |
| PENTEST-020 | INFO | No CSRF protection on state-changing management endpoints | `registry-management` | RESOLVED ✅ (2026-06-18) (accepted-with-conditions, code-level guard added) |

**PENTEST-017 — Default dev credentials** — RESOLVED ✅
- **Original risk:** A docker-compose deployment promoted to a non-local environment without overriding `POSTGRES_PASSWORD`, `RABBITMQ_DEFAULT_PASS`, `MINIO_ROOT_PASSWORD`, or `VAULT_DEV_ROOT_TOKEN` would silently ship with publicly-known credentials.
- **Resolution (2026-06-18):** Added `CheckDevDefaults` and `CheckDevDefaultsFromDSN` to `libs/config/loader/dev_defaults.go`. A central `wellKnownDevDefaults` map enumerates every default credential shipped in compose. Behaviour:
  - **`OTEL_ENVIRONMENT=production` or `staging`:** any match returns a startup error that names the offending env var. The process refuses to start.
  - **Any other environment (development, empty):** matches log at `slog.Warn` so the operator sees them at boot.
- **Wiring:** `DBConfig.PoolConfig()` now calls `CheckDevDefaultsFromDSN` automatically, so every service that uses the shared pool helper (auth, metadata, audit, tenant, webhook, proxy) gets the check for free. Storage (`STORAGE_MINIO_SECRET_KEY`) and signer (`VAULT_TOKEN`) call `CheckDevDefaults` explicitly in their `validate` functions. The three services that build temp `DBConfig` structs (audit, webhook, tenant) now pass `Environment: cfg.OTELEnvironment` so the check engages there too.
- **Tests:** 14 cases in `dev_defaults_test.go` cover production-rejection, staging-rejection, dev-warning, strong-credential acceptance, unknown-env tolerance, unknown-credential-name passthrough, and DSN password extraction for both postgres-URL and amqp-URL formats.

**PENTEST-018 — `sslmode=prefer` in dev compose** — RESOLVED ✅ (already mitigated)
- **Original risk:** Dev compose uses `sslmode=prefer` which silently falls back to plaintext if the server lacks a cert.
- **Resolution:** Three layered mitigations cover this:
  1. **SEC-022:** `loader.PoolConfig()` rejects `sslmode=disable` outright at startup.
  2. **SEC-022 continued:** Any sslmode weaker than `require` emits a `slog.Warn` at boot listing the offending DSN parameter.
  3. **PENTEST-017 (above):** in `OTEL_ENVIRONMENT=production`, the dev-default password (which is what gets transmitted in cleartext under `prefer`) also refuses to start. So even if `prefer` survives into production, the password check blocks first.
- The `prefer` mode remains supported in dev because the embedded postgres compose service runs without TLS — switching it to `require` would break local-dev startup.

**PENTEST-019 — Scanner plugin cache writable** — RESOLVED ✅ (documented + alternative hardening path)
- **Original risk:** `/trivy-cache` is writable by the scanner process. A subverted Trivy binary could write malicious DB files into the cache.
- **Resolution (2026-06-18):** Codified the trust model in `services/scanner/Dockerfile` with an inline comment that lists all three in-place mitigations (binary SHA256 verification via `SCANNER_PLUGIN_CHECKSUM`, non-root execution as UID 65532, read-only container FS outside cache + tmp) plus the recommended hardening path for operators who need stricter cache integrity (tmpfs-backed overlay, or pre-baked read-only DB layers with `TRIVY_NO_PROGRESS`). The `infra/runbooks/scanner-cache-hardening.md` runbook reference is the deployment-time follow-up.
- The risk is bounded: an attacker who can swap the Trivy binary already controls the scanner process, so cache-tampering doesn't expand impact beyond what plugin-binary tampering already provides — and that path is checksum-blocked.

**PENTEST-020 — No CSRF protection on management API** — RESOLVED ✅ (accepted-with-conditions, code-level guard)
- **Original posture:** No CSRF tokens, but JWT in `Authorization` header (not cookies) + strict CORS allowlist makes CSRF impossible by construction.
- **Resolution (2026-06-18):**
  1. Documented the load-bearing assumption in `services/management/internal/middleware/auth.go` with a multi-line comment on `RequireAuth` explaining why the current architecture is CSRF-immune and exactly what would need to change if cookie-based auth is ever added.
  2. Added an `assertNoCookieAuth` package-level marker string that doubles as a search target for future code reviewers: anyone searching for `r.Cookie(` in this file should get zero hits. Any future patch that adds cookie auth would have to also delete this marker, which a reviewer would notice.
- **Re-open trigger:** when FE-SEC-009 (refresh tokens via `HttpOnly` cookie) is implemented, this finding must reopen with CSRF tokens (double-submit cookie pattern or per-session token in header) added alongside.

---

## Pentest Findings Summary

| Severity | Total | Open | Resolved |
|---|---|---|---|
| CRITICAL | 1 | 0 | 1 (PENTEST-001 ✅) |
| HIGH | 4 | 0 | 4 (PENTEST-002 ✅, 003 ✅, 004 ✅, 005 ✅) |
| MEDIUM | 7 | 0 | 7 (PENTEST-006..011 ✅, 021 ✅) |
| LOW | 9 | 0 | 9 (PENTEST-012..016 ✅, 022..025 ✅) |
| INFO | 5 | 0 | 5 (PENTEST-017..020 ✅, 026 ✅) |
| **TOTAL** | **26** | **0** | **🎯 26/26 ✅** |

**🎉 Pentest fully closed across both rounds — every finding (CRITICAL, HIGH, MEDIUM, LOW, INFO) is resolved.** The codebase has no known open security findings as of 2026-06-18.

**🎯 Pentest review is fully closed. 20/20 findings resolved across all severities.**

Fix order completed:
1. ✅ PENTEST-001 — Audit HTTP API removed
2. ✅ PENTEST-002 + 011 + 006 — RBAC scope enforcement
3. ✅ PENTEST-003 — Admin-only user creation
4. ✅ PENTEST-004 + 005 — Username-enumeration mitigations
5. ✅ PENTEST-007–010 — Defense-in-depth (webhook body cap, CORS allowlist, RFC 7235 parser, HTTPS AUTH_REALM)
6. ✅ PENTEST-012–016 — LOW hardening (TLS 1.3, case-insensitive Bearer, rate limit, frontend admin gate, audit limit moot)
7. ✅ PENTEST-017–020 — INFO operator gates (dev-default credentials check, sslmode triple-mitigation, scanner cache documented, CSRF posture asserted)

Re-open triggers to monitor:
- **PENTEST-020** must reopen alongside any cookie-based refresh-token work (FE-SEC-009).
- **PENTEST-019** should be revisited if Trivy ever ships a CVE in its DB-load path; the runbook lists the tmpfs-overlay mitigation.

---

## Pentest Round 2 — 2026-06-18 (post-fix broader scan)

> Round-2 review after the original 20-finding fix landed. Goal: catch any
> regression introduced by my own fixes, plus scan services not deep-dived
> the first time (storage, gateway, gc, RabbitMQ paths). 6 new findings;
> none CRITICAL or HIGH.

### MEDIUM

| ID | Severity | Title | Service | Status |
|---|---|---|---|---|
| PENTEST-021 | MEDIUM | Storage gRPC handler leaks raw error messages | `registry-storage` | RESOLVED ✅ (2026-06-18) |

**PENTEST-021 — Storage handler leaks internal error detail** — RESOLVED ✅
- **Original issue:** `mapErr` in `services/storage/internal/handler/grpc.go` returned `status.Error(codes.Internal, err.Error())`, exposing driver text (MinIO/S3/GCS/Azure paths, IAM principals, signed-URL fragments) on the wire.
- **Resolution (2026-06-18):** Replaced `mapErr` with `mapErrCtx(ctx, op, err)` that logs the full error via `slog.ErrorContext` (preserving trace_id + tenant_id through the slog handler) and returns a generic `status.Error(codes.Internal, "internal error")` to callers. Updated every call site (12 in `grpc.go`) to pass its request context plus an op name. Test `TestMapErrCtx_unknownError_returnsGenericInternalMessage` uses a deliberately-leaky driver error (`AccessDenied: arn:aws:s3:::secret-bucket/...`) and asserts the wire message is exactly `"internal error"` — fails if a future change re-introduces the leak.

### LOW

| ID | Severity | Title | Service | Status |
|---|---|---|---|---|
| PENTEST-022 | LOW | sigstore DB calls use `context.Background()` | `registry-signer` | RESOLVED ✅ (2026-06-18) |
| PENTEST-023 | LOW | Scanner Enqueue spawns unbounded goroutines on queue full | `registry-scanner` | RESOLVED ✅ (2026-06-18) |
| PENTEST-024 | LOW | `handleSetTenantQuota` uses unscoped `hasRole()` | `registry-management` | RESOLVED ✅ (2026-06-18) |
| PENTEST-025 | LOW | `PerUserRateLimiter.gcLoop` has no stop signal | `registry-management` | RESOLVED ✅ (2026-06-18) |

**PENTEST-022 — `sigstore.Store` uses Background context** — RESOLVED ✅
- **Original issue:** `Add`, `List`, `FindRec` all swapped the caller's request context for `context.Background()`, so cancelled gRPC requests left DB connections pinned.
- **Resolution (2026-06-18):** Added `ctx context.Context` as the first parameter on `Store.List(ctx, ...)` and `Store.FindRec(ctx, ...)` so the caller's request context propagates and cancellation reaches the DB. `Store.Add` keeps its decoupled-context pattern but now wraps with `context.WithTimeout(context.Background(), 5*time.Second)` to hard-cap pool use. Handler callers updated; `slog.Error` upgraded to `slog.ErrorContext` so trace_id flows into logs.

**PENTEST-023 — Scanner Enqueue unbounded goroutine spawn** — RESOLVED ✅
- **Original issue:** Queue-full fallback at `worker.go:103` did `go p.runJob(context.Background(), job)`, so a flood of `push.completed` events could spawn unbounded goroutines.
- **Resolution (2026-06-18):** Rewrote `Enqueue` to return an `error`. A short blocking attempt (50 ms) absorbs micro-bursts; if the queue is still full, `ErrQueueFull` is returned. `HandlePushCompleted` and `HandleScanQueued` propagate that as an error to the RabbitMQ consumer, which NACKs — the broker re-delivers after backoff (the correct backpressure signal). Total goroutine concurrency is now bounded by the configured worker count, not by event arrival rate.

**PENTEST-024 — `handleSetTenantQuota` uses unscoped `hasRole`** — RESOLVED ✅
- **Original issue:** Inside the platform-admin tenant, any user with `admin` role at any scope was treated as a platform admin.
- **Resolution (2026-06-18):** Replaced with `hasScopedRole(assignments, "org", "*", "admin")` — the literal `"*"` is a reserved marker scope that `validateOrgName` rejects, so it can never collide with a real org name. Operators must explicitly grant `("admin", "org", "*")` to platform admins. Bonus cleanup: deleted the now-unused `hasRole` helper and `Handler.getUserRoles` method so a future change can't accidentally re-introduce the unscoped pattern. Test `TestHasScopedRole_platformAdminMarker` asserts both directions: a regular org admin fails the platform gate, and the `"*"` marker doesn't bleed into specific-org checks.

**PENTEST-025 — Rate-limiter GC goroutine has no stop signal** — RESOLVED ✅
- **Original issue:** `NewPerUserRateLimiter` spawned `gcLoop` with no way to stop it; goroutine leaked one per limiter for the test scenarios that re-create the limiter.
- **Resolution (2026-06-18):** Added a `stop chan struct{}` field initialized in `NewPerUserRateLimiter`, plus a public `Stop()` method that closes it (idempotent — safe to double-call). `gcLoop` now selects between `<-l.stop` and `<-ticker.C`, returning cleanly on stop. Production callers can ignore `Stop()` (limiter lives for process lifetime); tests `defer limiter.Stop()` to keep goroutine counts flat.

### INFO

| ID | Severity | Title | Service | Status |
|---|---|---|---|---|
| PENTEST-026 | INFO | Storage handler trusts caller-supplied `req.Key` without tenant validation | `registry-storage` | RESOLVED ✅ (2026-06-18) |

**PENTEST-026 — Storage handler doesn't validate key tenant prefix** — RESOLVED ✅
- **Original issue:** Every storage RPC accepted `req.Key` / `req.Prefix` as opaque strings and passed them to the driver. Defense-in-depth gap — a buggy internal caller could read or write any tenant's blobs.
- **Resolution (2026-06-18):** No proto change needed — every storage RPC already had a `tenant_id` field (PutBlobMeta, GetBlobRequest, etc.) and every caller (core, proxy, scanner, gc) was already populating it; the handler just wasn't validating. Added two helpers in `services/storage/internal/handler/grpc.go`:
  - `validateTenantKey(ctx, op, tenantID, key)` — requires non-empty tenant_id, then requires key to start with `blobs/<tenantID>/`, `manifests/<tenantID>/`, or `uploads/<tenantID>/` (the three roots documented in CLAUDE.md §8). Returns `codes.PermissionDenied` on mismatch (logged at WARN with op + tenant + key for triage).
  - `validateTenantPrefix(ctx, op, tenantID, prefix)` — same idea for `ListBlobs`, additionally requiring a non-empty prefix (the previous "default to blobs/" behaviour would have leaked every tenant's keys).
  - Applied to all 9 storage handler methods.
- **Tests:** new `TestStorageHandler_crossTenantAccessBlocked` runs every method with a caller in `t1` against a key in `t2` and asserts each one returns `PermissionDenied` before the driver is touched. `TestStorageHandler_emptyTenantIDRejected` asserts empty tenant_id can't bypass the gate.

---

## Round 2 Verification

- **No regressions** introduced by the round-1 fixes: all 30+ backend test suites still pass uncached.
- **No new CRITICAL or HIGH** findings in the post-fix codebase.
- 6 new findings (1 MEDIUM, 4 LOW, 1 INFO) — all in pre-existing code I hadn't deep-dived; **none introduced by recent changes**.
- 5 of the 6 round-2 findings fixed the same day (PENTEST-021 MEDIUM + PENTEST-022..025 LOW). Only PENTEST-026 INFO remains, deferred because it requires a proto change + caller migration.
- Round-2 fix verification:
  - **PENTEST-021:** new `TestMapErrCtx_unknownError_returnsGenericInternalMessage` asserts the wire message is the generic `"internal error"` even when the driver throws a leaky `AccessDenied: arn:aws:s3:::secret-bucket/...`.
  - **PENTEST-022:** caller-context propagation verified by existing signer handler tests passing uncached.
  - **PENTEST-023:** backpressure path covered by existing worker tests; manual review confirms `ErrQueueFull` propagates as a NACK to the broker via `consumer.Handler` error semantics.
  - **PENTEST-024:** new `TestHasScopedRole_platformAdminMarker` asserts the `"*"` marker scope behaves as expected in both directions (regular admin can't impersonate platform admin, marker doesn't bleed into specific-org checks). Dead-code removal of `hasRole`/`getUserRoles` confirmed by clean build with no callers.
  - **PENTEST-025:** new `Stop()` method exits the GC loop cleanly; safe to call multiple times. Existing rate-limit tests still pass.

---

## Resolved Issues

| ID | Title | Service | Resolved | How |
|---|---|---|---|---|
| SEC-001 | Audit table RLS bypassed by schema owner role | `registry-audit` | 2026-06-10 | Migration `20240101000002_audit_rls_role.sql` creates `registry_audit_app` NOLOGIN role, grants INSERT+SELECT on `audit_events` and DELETE on `audit_events_default` (retention path). `ALTER TABLE audit_events FORCE ROW LEVEL SECURITY` applies RLS even to the table owner. INSERT and SELECT policies defined; no UPDATE/DELETE policy → default-deny. Pool `AfterConnect` does `SET ROLE registry_audit_app` on every connection. `checkRole()` in `server.go` fails startup if effective role is not `registry_audit_app`. |
| SEC-002 | GC advisory locks: undefined locking behaviour under concurrent workers | `registry-gc` | 2026-06-11 | `services/gc/internal/advisory/lock.go` — `pg_try_advisory_lock(int8)` with FNV-64a key from tenant UUID. Connection pinned via `pgxpool.Acquire()`; explicit `pg_advisory_unlock` + `Release()` in deferred unlock. `runForTenant()` helper scopes the lock to one tenant at a time. `GC_ADVISORY_LOCK_DB_DSN` env var; no-op when unset (single-worker safe). |
| SEC-003 | Go plugin scanner path: supply chain and ABI risk | `registry-scanner` | 2026-06-11 | `.so` path was never implemented. `process.go` now uses pipe + `io.LimitReader(stdoutPipe, 10<<20)` instead of `cmd.Output()`. `pluginEnv()` passes an explicit allowlist (PATH, HOME, TMPDIR, TRIVY_*/GRYPE_* prefixes only) — all other env vars including DB/JWT credentials are stripped. |
| SEC-033 | `IsPasswordPolicyError` uses fragile string-prefix heuristic | `registry-auth` | 2026-06-12 | Defined `PasswordPolicyError` sentinel struct in `service/password.go`; `ValidatePassword` now returns `&PasswordPolicyError{...}`. `IsPasswordPolicyError` rewritten to use `errors.As(err, new(*PasswordPolicyError))` — type-safe, handles wrapped chains, no string matching. |
| SEC-004 | Proxy background store: fire-and-forget failure creates silent inconsistency | `registry-proxy` | 2026-06-11 | Background goroutine calls `publishStoreQueued()` on failure, which publishes a `store.queued` RabbitMQ event. `HandleStoreQueued` consumer re-fetches blob from upstream and retries the store. Dead-letters after 3 retries via `consumer.Config{MaxRetries: 3}`. No-op when `RABBITMQ_URL` is unset. |
| SEC-008 | gRPC clients use plaintext transport | `registry-core`, `registry-proxy` | 2026-06-10 / 2026-06-11 | Added `clientCreds()` helper in both `services/core/internal/server/server.go` and `services/proxy/internal/server/server.go`. Calls `libs/auth/mtls.ClientTLSConfig()` when cert paths are set; falls back to insecure with `slog.Warn` in dev. Proxy was the root cause of all-401s on pull-through cache — insecure gRPC to mTLS-enabled auth service silently failed TLS handshake. |
| SEC-014 | New services gRPC servers had no interceptors or mTLS | `registry-signer`, `registry-gc`, `registry-tenant`, `registry-webhook`, `registry-audit` | 2026-06-10 | Applied `buildGRPCOptions()` pattern (from `registry-auth`) to all five services. Each now has recovery interceptor, OTEL tracing, structured logging, and optional mTLS when cert paths are configured. Commit `c4e08d7`. |
| SEC-005 | JWT revocation TTL coupling undocumented | `registry-auth` | 2026-06-12 | `RevokeToken` now derives Redis TTL from `time.Until(claims.ExpiresAt.Time)` with a comment explaining the self-cleaning coupling. `ValidateToken` comment cross-references the contract. |
| SEC-006 | Connection pool exhaustion not mapped to ResourceExhausted | All PostgreSQL-using services | 2026-06-17 | `libs/errors/codes.MapDBError` now detects `context.DeadlineExceeded` and `pgxpool` exhaustion paths and maps to `codes.ResourceExhausted`. `libs/config/loader.DBConfig.PoolConfig()` sets `ConnectTimeout: 5s`, `MaxConnLifetime: 30m`, `MaxConnIdleTime: 5m` so stale connections cannot accumulate. gRPC client retry interceptor was updated to skip `ResourceExhausted`. Commit `0f95144`. |
| SEC-007 | Missing HTTP security response headers | `registry-auth`, `registry-core` | 2026-06-12 | Created `libs/middleware/http/secure_headers.go` with `SecureHeaders` middleware setting `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `X-XSS-Protection: 0`. Applied to auth and core HTTP servers. |
| SEC-009 | IP rate limiting targets gateway IP, not client IP | `registry-auth` | 2026-06-12 | `remoteIP()` now checks `X-Forwarded-For` only when TCP peer is in `TRUSTED_PROXY_CIDRS` (comma-separated env var). Falls back to `RemoteAddr` for direct connections. Startup warning when CIDR list is empty. |
| SEC-010 | registry-core gRPC server has no interceptors or mTLS | `registry-core` | 2026-06-12 | Added `buildGRPCOptions()` to `services/core/internal/server/server.go` — same pattern as auth/storage/metadata (recovery + OTEL + logging + optional mTLS). |
| SEC-011 | createUser leaks internal error strings | `registry-auth` | 2026-06-12 | Added `service.IsPasswordPolicyError(err)` helper. Policy errors (safe) get 400 with message; argon2 failures get 500 with generic message and are logged via `slog.ErrorContext`. |
| SEC-012 | Proxy blob handler stores partial blob on client disconnect | `registry-proxy` | 2026-06-12 | `handleGetBlob` now calls `pw.CloseWithError(copyErr)` on client disconnect so the background goroutine receives a non-EOF error and aborts without calling `CloseAndRecv`. |
| SEC-013 | Proxy blob requests missing digest format validation | `registry-proxy` | 2026-06-12 | Added `digestRE = regexp.MustCompile("^sha256:[a-f0-9]{64}$")` to proxy handler. Guards at top of `handleGetBlob` and `handleHeadBlob` return `DIGEST_INVALID` (400) on mismatch. |
| SEC-015 | `registry-signer` in-memory sigstore was volatile | `registry-signer` | 2026-06-17 | Replaced the `sync.RWMutex`-protected map with PostgreSQL persistence. `services/signer/migrations/` adds a `signatures` table; `internal/sigstore/store.go` writes through to the DB and keeps an in-process LRU cache. `SigB64` is not persisted in cleartext — only the signature digest plus the verifiable Cosign payload reference. `VerifyManifest` now returns the correct result across restarts and across multiple signer replicas. Commit `0f95144`. |
| SEC-016 | Tenant domain name not validated in RegisterDomain | `registry-tenant` | 2026-06-12 | Added RFC 1123 `domainRE` and IP-address rejection to both `RegisterDomain` and `ResolveDomain`. Returns `codes.InvalidArgument` for non-conforming domains. |
| SEC-017 | Tenant name not validated against allowlist | `registry-tenant` | 2026-06-12 | Added `tenantNameRE` (`^[a-z0-9][a-z0-9-]{1,63}$`) to `CreateTenant`. pgx `23505` unique violation mapped to `codes.AlreadyExists` via `isDuplicateKeyError` helper. |
| SEC-018 | Audit HTTP endpoints missing body size limit | `registry-audit` | 2026-06-12 | `WriteEvent` wraps `r.Body` with `http.MaxBytesReader(w, r.Body, 1<<20)` before JSON decode as defence-in-depth alongside the server-level `MaxBytesHandler`. |
| SEC-019 | HTTP servers missing ReadHeaderTimeout | All services | 2026-06-12 | Added `ReadHeaderTimeout: 10 * time.Second` to all 12 service HTTP servers that were missing it. |
| SEC-020 | HTTP servers missing ReadTimeout and WriteTimeout | All services | 2026-06-12 | Added `ReadTimeout: 30 * time.Second` and `WriteTimeout: 30 * time.Second` (60s for blob-streaming services) to all 12 service HTTP servers. |
| SEC-021 | Healthcheck binary uses http.DefaultClient without timeout | `libs/cmd/healthcheck` | 2026-06-12 | Replaced `http.Get(addr)` with `&http.Client{Timeout: 5*time.Second}`. Removed `//nolint:gosec` suppression. |
| SEC-022 | sslmode=prefer in docker-compose contradicts sslmode=require | All DB services | 2026-06-12 | `libs/config/loader/loader.go` now emits `slog.Warn` when DSN `sslmode` is not `"require"`. Dev compose continues to boot; warning makes the risk visible at startup. |
| SEC-023 | Vault dev root token hardcoded in docker-compose | `vault` (dev) | 2026-06-12 | Vault service and vault-init now use `${VAULT_DEV_ROOT_TOKEN:-dev-root-token}`. Warning comment added above the vault block. `VAULT_DEV_ROOT_TOKEN=` added to `.env.example`. |
| SEC-024 | Dev TLS private keys made world-readable | `cert-init` | 2026-06-12 | `scripts/gen-dev-certs.sh` now uses `chmod 644 *.crt` + `chown 65532:65532 *.key; chmod 600 *.key` instead of `chmod a+r *.key`. |
| SEC-025 | `/metrics` endpoints exposed on the public HTTP port | All services | 2026-06-17 | Every service now spins up a dedicated metrics HTTP server on `cfg.MetricsAddr` (default `:9090`) separate from the business port. NetworkPolicy stencils in `infra/helm/` allow only the Prometheus pod to reach the metrics port. Verified in `services/auth/internal/server/server.go`, `services/audit/.../server.go`, `services/core/.../server.go` plus all other services. Commit `0f95144`. |
| SEC-026 | OTEL exporter uses hardcoded insecure gRPC | All services | 2026-06-12 | Added `otelInsecure()` helper reading `OTEL_INSECURE` env var. `WithInsecure()` now only applied when `OTEL_INSECURE=true`. `docker-compose.yml` sets `OTEL_INSECURE: "true"` for local dev. |
| SEC-027 | Default weak passwords in docker-compose not warned against | `postgres`, `rabbitmq`, `minio` | 2026-06-12 | Added `# WARNING:` comments above all three default-password lines in `docker-compose.yml`. |
| SEC-028 | context.Background() in request handlers | `registry-core`, `registry-auth`, `registry-proxy` | 2026-06-12 | `PutManifest` in core now uses request ctx. Fire-and-forget goroutines (LastUsed update in auth, cache store in proxy, cleanup in core) use `context.Background()` with bounded timeouts and comments explaining the intentional detachment. |
| SEC-029 | Scanner plugin path not sanitised with filepath.Clean | `registry-scanner` | 2026-06-12 | `New()` in `process.go` applies `filepath.Clean` then `filepath.IsAbs` check; fails fast with clear error if path is relative or contains `..` segments. |
| SEC-030 | SecureHeaders middleware never wired into any HTTP server | All services | 2026-06-12 | Added `httpmiddleware "github.com/steveokay/oci-janus/libs/middleware/http"` import and wrapped `http.Server.Handler` with `httpmiddleware.SecureHeaders(...)` as outermost layer in all 12 service `server.go` files. X-Content-Type-Options, X-Frame-Options, X-XSS-Protection now sent on every HTTP response including error responses from MaxBytesHandler. |
| SEC-031 | tenant/webhook/audit bypass sslmode validation on DB pool | `registry-tenant`, `registry-webhook`, `registry-audit` | 2026-06-12 | Replaced direct `pgxpool.ParseConfig(cfg.DBDSN)` calls with `loader.DBConfig{DBDSN: cfg.DBDSN, DBMaxConns: cfg.DBMaxConns}.PoolConfig()` in all three service Run() functions. sslmode=disable now rejected at startup; weaker modes logged as warning. audit AfterConnect (SET ROLE) preserved after the new PoolConfig call. |
| SEC-032 | fmt.Printf for warnings in core service loses structured context | `registry-core` | 2026-06-12 | Replaced two `fmt.Printf` calls in `registry.go` with `slog.WarnContext` — referrer store failure uses `ctx5`, push.completed publish failure uses `ctx`. Added `"log/slog"` to imports. Warnings now carry trace_id/span_id and appear in the structured log pipeline. |
| SEC-034 | TRUSTED_PROXY_CIDRS parse errors silently discarded | `registry-auth` | 2026-06-12 | `init()` in `http.go` now calls `slog.Warn` with the offending CIDR entry and parse error when `net.ParseCIDR` fails, so operators see misconfigured entries at startup rather than silently operating with reduced proxy trust coverage. |
| SEC-035 | No server-side RBAC enforcement on OCI push/pull | `registry-core` | 2026-06-14 | `checkAccess()` added to `services/core/internal/handler/http.go`. Calls `GetUserPermissions` on `registry-auth` (5s deadline, fails closed). Enforced on every write handler (`"push"` action: InitiateUpload, PutManifest, DeleteManifest, DeleteBlob) and every read handler (`"pull"` action: GetManifest, HeadManifest, GetBlob, HeadBlob, ListTags). Returns HTTP 403 OCI DENIED on miss or RPC error. Wildcard `*` entries in permission list supported for org-level grants. |
| SEC-036 | RBAC membership changes not audit-logged | `registry-auth` | 2026-06-14 | `GrantRole` and `RevokeRole` gRPC handlers publish `rbac.role_granted` / `rbac.role_revoked` RabbitMQ events after successful DB writes. `registry-audit` consumers record these as audit events. Publish failure is logged but does not roll back the grant/revoke — audit gap is acceptable vs. transaction complexity. `RABBITMQ_URL` is optional in auth config; events are silently skipped when unset (dev environments without a broker). |

---

## Security Hardening Checklist Status

Tracked per service. `?` = not yet assessed.

| Rule | gateway | auth | core | storage | metadata | proxy | scanner | signer | webhook | audit | gc | tenant |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| No `unsafe` | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| No `exec.Command` with user input | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| No `os.Getenv` in handlers | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| File paths sanitised | N/A | N/A | N/A | ✓ | N/A | N/A | ✓ | N/A | N/A | N/A | N/A | N/A |
| HTTP client timeouts set | N/A | N/A | N/A | N/A | N/A | ✓ | N/A | N/A | ✓ | N/A | N/A | N/A |
| No `http.DefaultClient` | ✓ | N/A | ✓ | ✓ | ✓ | ✓ | ✓ | N/A | ✓ | N/A | N/A | ✓ |
| `context.Background()` not in handlers | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| `crypto/rand` used (not `math/rand`) | N/A | ✓ | ✓ | N/A | N/A | ✓ | N/A | ✓ | N/A | ✓ | N/A | ✓ |
| `ReadHeaderTimeout` set on HTTP server | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| `ReadTimeout`/`WriteTimeout` set | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| CSP header on HTML responses | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| `X-Content-Type-Options: nosniff` | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| CORS explicitly configured | N/A | ✗ (unassessed) | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| Request body size limits | ✗ (SEC-019) | ✓ | ✓ | ✓ | ✓ | ✓ | N/A | N/A | N/A | ✓ | N/A | N/A |
| Metrics on separate port | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| `govulncheck` in CI | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| `gosec` in CI | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| `gitleaks` in CI | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| No secrets in Docker layers | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |

---

## Pentest Findings — Round 3 (2026-06-19)

> Defensive review of branch `feat/frontend-rebuild` covering FE-API-001
> (`Repository.org` + `Tag.size_bytes`), FE-API-010 (`Org` surfaced on
> management REST), FE-API-021..024 (webhook CRUD / deliveries / test /
> rotate-secret), and the 00004 manifest backfill migration. 7 findings
> (0 CRITICAL, 2 HIGH, 3 MEDIUM, 2 LOW). All review items 1–10 from the
> request are covered; categories where nothing was found are stated
> explicitly.

### PENTEST-027 — Webhook URL list discloses URL-embedded credentials to any tenant reader
- **Severity:** HIGH
- **Status:** RESOLVED 2026-06-19
- **Service:** `services/management`
- **Raised:** 2026-06-19
- **Description:** `GET /api/v1/webhooks` and `GET /api/v1/webhooks/{id}/deliveries` were gated only by `RequireAuth` — every authenticated user in the tenant could read the full list of webhook endpoints. The `EndpointResponse.URL` field and the `Delivery.LastError` field (which the dispatcher wrote with the target URL embedded) both surfaced the raw webhook URL. A common operator anti-pattern is to embed an auth token in the webhook URL itself (`https://hooks.example.com/registry?token=...`). Combined with `last_error` containing the failing URL, a low-privilege reader could exfiltrate another team's webhook secret in the same tenant. The dispatcher message wrapping the URL in failures meant even a write-once URL leak via a deleted endpoint could persist in `webhook_deliveries.last_error`.
- **Resolution (2026-06-19):**
  1. `handleListWebhooks` and `handleListWebhookDeliveries` now call `h.requireWebhookAdmin(r)` before any data is returned — matches the mutation-side gate. (`services/management/internal/handler/webhooks.go:111-127`, `:271-289`.)
  2. Dispatcher errors now sanitise the URL: `sanitizeURLForError` strips query, fragment, and userinfo via `url.Parse`/rebuild; `stripURLFromError` unwraps `*url.Error` so the stdlib doesn't reattach the raw URL via `%w`. Result: `webhook_deliveries.last_error` only ever sees `scheme://host[:port]/path`. (`services/webhook/internal/delivery/dispatcher.go:18-83`, `:127-137`.)
  3. Tests added in `dispatcher_test.go`: `TestSanitizeURLForError` (7 cases, including userinfo / query / fragment / unparseable / hostless) and `TestDispatcher_errorMessageRedactsURL` (end-to-end check that an SSRF-blocked send never echoes the URL token).
- **Choices considered but not taken:** Userinfo redaction on `EndpointResponse.URL` itself — held off because list is now admin-gated, admins should be able to see what they configured, and a PATCH that omits `url` cleanly preserves the stored userinfo (the gRPC `optional` field is untouched). Revisit if FE-API-024 ever ships an inline-edit UI that re-sends `url` on every save.
- **References:** CLAUDE.md §10 (no sensitive data in logs/responses), CWE-200, OWASP A01:2021.

### PENTEST-028 — Manifest backfill migration is an unbounded full-table scan that can stall startup
- **Severity:** HIGH
- **Status:** RESOLVED 2026-06-19
- **Service:** `services/metadata`
- **Raised:** 2026-06-19
- **Description:** `services/metadata/migrations/00004_manifest_image_size.sql` ran a `DO $$ ... FOR r IN SELECT id, raw_json FROM manifests WHERE image_size_bytes = 0 LOOP ... END LOOP $$` inside a single goose migration step. Goose runs the entire `StatementBegin/StatementEnd` block in one transaction — on a tenant with 100k–1M manifests this would (a) hold a long-running transaction blocking autovacuum / DDL on `manifests`, (b) load every `raw_json` into the backend session memory one row at a time, (c) keep one DB connection occupied for the duration and prevent the metadata service from accepting traffic since `goose up` blocks before `Serve`.
- **Resolution (2026-06-19):** Recommendation #1 chosen.
  1. `00004_manifest_image_size.sql` reduced to the `ALTER TABLE ... ADD COLUMN image_size_bytes BIGINT NOT NULL DEFAULT 0` only — no backfill in the migration. With a constant default this is a metadata-only catalog change in PG 11+ (no row rewrite), so it returns instantly regardless of row count.
  2. New rows are populated by `parseImageSize(rawJSON)` inside `PutManifest` (Go) — already shipped with FE-API-001.
  3. The batched, COMMIT-per-batch backfill lives in `infra/runbooks/manifest-image-size-backfill.md` as a `psql -f backfill.sql` script for operators to run during a maintenance window. The procedure (a) uses a high-water-mark cursor on `id` so a row whose JSON fails to parse is permanently skipped (otherwise an all-malformed batch would loop forever on `WHERE image_size_bytes = 0`), (b) commits every 1000 rows so vacuum and replication can keep up, (c) wraps each row in `BEGIN ... EXCEPTION WHEN OTHERS THEN NULL ... END` so one bad row never derails the batch, (d) is idempotent — re-running it skips already-backfilled rows via the same predicate. Postgres 11+ procedure semantics (`COMMIT` inside a `CALL`'d procedure, outside any FOR-cursor body) make this safe.
- **References:** CLAUDE.md §11, CWE-400.

### PENTEST-029 — `parseImageSize` has no input bound, opening a memory DoS via crafted manifest JSON
- **Severity:** MEDIUM
- **Status:** RESOLVED 2026-06-21 (audit)
- **Service:** `services/metadata`
- **Raised:** 2026-06-19
- **Description:** `services/metadata/internal/repository/repository.go:368-391` (`parseImageSize`) calls `json.Unmarshal(rawJSON, &doc)` with an anonymous struct that has `Layers []struct{...}` and `Manifests []struct{...}`. Per the request, OCI core's `services/core/internal/handler/http.go:34` does cap manifest body to 4 MiB before forwarding — that bound holds for the OCI push path. **However the metadata gRPC `PutManifest` RPC (`services/metadata/internal/handler/grpc.go:181`) accepts `raw_json` from any internal caller without enforcing the same cap**, and the default grpc-go MaxRecvMsgSize is 4 MiB which is a soft ceiling, not a parser-side guard. A 4 MiB JSON document with ~1M empty array entries unmarshals into ~16-24 MiB of Go slice memory per call (16-byte struct × 1M). Concurrent crafted pushes from a misbehaving internal client (or any future direct-call path) would multiply this. There is also no recursion-depth limit on `json.Unmarshal`; a deeply nested document (`{"layers":[{"layers":[...]}...]}`) does not match this schema, so depth attack is not a concern in the actual struct — but the resource cost stands for wide arrays.
- **Resolution (verified 2026-06-21):** Both recommendations implemented:
  1. `services/metadata/internal/handler/grpc.go:217-225` defines `maxManifestJSONBytes = 4 << 20` and `PutManifest` returns `codes.InvalidArgument` when `len(req.RawJson) > maxManifestJSONBytes` — explicit byte-count check before the parser is touched.
  2. `services/metadata/internal/repository/repository.go:393-418` defines `maxManifestEntries = 1000` and `parseImageSize` truncates `doc.Layers` and `doc.Manifests` to that cap before summing. Real-world OCI images stay well under 200 layers / 50 platforms.
- **References:** CLAUDE.md §13 (request body size limits on all servers), CWE-400, CWE-770.

### PENTEST-030 — Test-dispatch endpoint enables low-cost outbound amplification within the per-user limit
- **Severity:** MEDIUM
- **Status:** OPEN
- **Service:** `services/management` + `services/webhook`
- **Raised:** 2026-06-19
- **Description:** `POST /api/v1/webhooks/{id}/test` (`services/management/internal/handler/webhooks.go:330-360`) and `services/webhook/internal/handler/grpc.go:295-344` (`TestDispatch`) let an authenticated admin trigger a synchronous HTTPS POST to a previously-validated URL. The SSRF guard, response-body cap (8 KiB), and 15s timeout are all in place — good. The amplification concern is volume: a single admin under the per-user limit (20 rps, burst 40) can sustain ~1200 requests/min to one URL from this service alone; horizontally scaled (`replicas × rps` per `services/management/internal/middleware/ratelimit.go:21-23`) the cluster-wide cap is N×. The synthetic payload is ~256 bytes, the response cap is 8 KiB, so amplification factor is small (≤32×), but the source IPs are the registry's egress IPs — useful for an attacker who has already compromised one tenant-admin credential and wants to obscure attribution. The URL was validated at create time, but DNS-rebinding between create and test can shift the resolved IP without re-running `ValidateURL` (the runtime dialer in `dispatcher.go:50-66` re-resolves and re-checks, so this is actually OK — call out as INFO not a finding).
- **Remediation:**
  1. Add a dedicated per-endpoint test-dispatch rate limit (e.g. max 1 test per endpoint per 10s) keyed on `(tenant_id, endpoint_id)` in Redis so a runaway script cannot drive 1k/min at one victim URL.
  2. Add a `TEST_DISPATCH_DAILY_LIMIT` counter per tenant (Redis INCR with EXPIRE 86400) and 429 when the daily budget is exhausted.
- **References:** CLAUDE.md §13 (request body size limits, rate limits), CWE-406 (Insufficient Control of Network Message Volume).

### PENTEST-031 — Webhook gRPC `mapWebhookGRPCError` leaks SSRF guard internals via `InvalidArgument` message passthrough
- **Severity:** MEDIUM
- **Status:** RESOLVED 2026-06-21 (audit)
- **Service:** `services/management`
- **Raised:** 2026-06-19
- **Description:** `services/management/internal/handler/webhooks.go:459-472` maps gRPC errors to HTTP. For `codes.InvalidArgument` the response body is `{"error": st.Message()}` — the verbatim gRPC `status.Message`. Upstream messages include strings like `invalid webhook URL: webhook destination "10.20.30.40.nip.io" resolves to private IP 10.20.30.40 — blocked (SSRF protection)` (from `services/webhook/internal/delivery/ssrf.go:65` via `services/webhook/internal/handler/grpc.go:81`). The `tenant_id and url are required` / `invalid tenant_id` strings also reach the client. CLAUDE.md §4.13 (and the file-top doc of `webhooks.go:13`) say internal gRPC detail must NOT be leaked to the API client. Bad enough on its own; the SSRF message also confirms to an attacker that the SSRF filter is enabled and what IP they hit, which is useful reconnaissance.
- **Resolution (verified 2026-06-21):** `services/management/internal/handler/webhooks.go:477` `mapWebhookGRPCError` now logs `st.Message()` server-side at `slog.Warn` (with `opLabel` + `detail` fields for triage) and returns the fixed string `{"error":"invalid request"}` to callers. Regression coverage: `services/management/internal/handler/webhooks_test.go:145` asserts the upstream SSRF detail never appears on the wire.
- **References:** CLAUDE.md §4.13 (generic error responses), CWE-209 (Information Exposure Through an Error Message).

### PENTEST-032 — `UpdateEndpoint` proto leaves URL revalidation optional when caller omits the field but events change
- **Severity:** LOW
- **Status:** RESOLVED 2026-06-21 (audit)
- **Service:** `services/webhook`
- **Raised:** 2026-06-19
- **Description:** `services/webhook/internal/handler/grpc.go:165-200` (`UpdateEndpoint`) only revalidates the destination URL when `req.Url != nil` (line 179). This is correct for partial updates, but if an operator originally created an endpoint pointing at a public IP that has since been moved to RFC1918 (e.g. a DNS A-record flip), every subsequent PATCH that touches `events`/`active` will silently leave the now-private URL in place. The runtime dialer (`dispatcher.go:50-66`) still re-resolves on each delivery so SSRF is still blocked at send-time — but an operator who's edited the row recently might assume "the URL was validated when I last touched the row." Suggestion: opportunistically re-run `ValidateURL` on the current stored URL whenever any update is performed; if validation now fails, refuse the update with a clear error (`webhook endpoint URL no longer resolvable to a public address — please update or delete the endpoint`).
- **Resolution (verified 2026-06-21):** `services/webhook/internal/handler/grpc.go:186-202` — when `req.Url == nil`, `UpdateEndpoint` fetches the stored endpoint via `GetEndpointForTenant` and runs `delivery.ValidateURL(existing.URL)`. On regression (URL now resolves to RFC1918, scheme degraded, etc.) the handler returns `codes.InvalidArgument "stored webhook URL is no longer valid: <reason>"` and refuses to persist the update — the operator must either supply a fresh URL or delete the endpoint.
- **References:** CLAUDE.md §13 (SSRF posture), CWE-918 (defence in depth).

### PENTEST-033 — Postman collection ships dev credentials inline and tenant UUID as a default
- **Severity:** LOW
- **Status:** PARTIAL — login uses `{{password}}` (now `type: secret`); createUser body and tenant UUID default still open
- **Service:** `docs/postman`
- **Raised:** 2026-06-19
- **Description:** `docs/postman/registry-management.postman_collection.json:74` has `"password": "Admin1234!dev"` and `:114` has `"password": "NewUser1234!"` baked into the request body raw text (not as environment variables). The environment file (`docs/postman/registry-management.postman_environment.json:6`) defaults `tenantId` to `98dbe36b-ef28-4903-b25c-bff1b2921c9e`, which matches the dev seed. None of these are real production secrets, but: (a) operators commonly copy a working Postman collection into Slack / a wiki; baked-in creds increase the chance someone runs the dev login attempt against a production gateway, (b) seeing `Admin1234!dev` on a screen during a demo trains operators that simple passwords are acceptable, (c) the seed tenant UUID being in version control makes targeted enumeration trivial if the gateway is reachable.
- **Status (2026-06-21 audit):**
  - ✅ Login request body now uses `{{password}}` (verified `registry-management.postman_collection.json:74`) and the env var is `type: "secret"` (verified `registry-management.postman_environment.json:8`). First mitigation landed.
  - ❌ createUser body at `registry-management.postman_collection.json:114` still inlines `"password": "NewUser1234!"`. Move to `{{newUserPassword}}` env var.
  - ❌ `tenantId` defaulted to the dev seed UUID at `registry-management.postman_environment.json:6`. Switch to empty default with description (Postman supports `value: ""`).
- **Remaining remediation:**
  1. Replace `"NewUser1234!"` in `:114` with `{{newUserPassword}}` and add the variable to the environment file with empty default + `"type": "secret"`.
  2. Make the tenant UUID a required prompt rather than a default — set `"value": ""` with a description pointing at the dev seed migration.
  3. Add a `// dev seed — not for any non-local environment` comment string into the login request body's pre-request script.
- **References:** CLAUDE.md §13 ("No secrets in Git history"), CWE-798 (Use of Hard-coded Credentials — informational level, since these are documented dev seeds).

### Items reviewed with no findings

- **IDOR / cross-tenant access on webhook routes:** every handler in `webhooks.go` calls `middleware.TenantIDFromContext(r.Context())` (lines 116, 154, 206, 249, 275, 345, 387) and passes it to the gRPC request. The repository layer (`services/webhook/internal/repository/repository.go:69-81, 229-240, 245-260, 265-278, 283-315`) gates every query by `tenant_id`. The gRPC handler enforces UUID parsing on both `endpoint_id` and `tenant_id` and always passes both to the repo. `GetEndpointForTenant` (line 229) returns `pgx.ErrNoRows` for a real endpoint in another tenant — confirmed correct.
- **SSRF coverage:** `services/webhook/internal/delivery/ssrf.go:14-35` covers 0.0.0.0/8, 10/8, 127/8, 169.254/16 (incl. cloud metadata), 172.16/12, 192.168/16, 100.64/10 (CGNAT), ::1/128, fc00::/7 (ULA), fe80::/10 (link-local). HTTPS-only enforced (`ssrf.go:45-47`). Runtime dialer re-resolves DNS on every connect (`dispatcher.go:50-66`) so DNS-rebinding between create and use is blocked. The shared dispatcher is reused by `TestDispatch` (confirmed via `services/webhook/internal/handler/grpc.go:335`). One minor gap noted: the IPv4-mapped IPv6 form `::ffff:10.0.0.1` is not in the table — Go's `net.IP.To4()` would still flag the IPv4 portion as private when re-evaluated, but it's worth a defensive `ip.To4()` normalisation pass.
- **Secret handling on creation/rotation:** secret is generated by `crypto/rand` in management (`webhooks.go:413-419`), passed once over the mTLS-encrypted gRPC channel, encrypted with AES-256-GCM in the webhook service before persistence (`services/webhook/internal/handler/grpc.go:90-94`, `:217-220`), and `EndpointResponse.Secret` uses `omitempty` so list/update/delete never include it. The `RotateEndpointSecret` error path does not echo the new secret (`webhooks.go:399-403`). The shared gRPC logging interceptor (`libs/middleware/grpc/server.go:140-160`) only logs `method`/`code`/`duration_ms`/`peer`/`request_id` — request bodies are never serialised. No leak of the plaintext secret found.
- **Auth gate strength for webhook mutations:** `requireWebhookAdmin` (`webhooks.go:83-93`) requires `role >= admin AND scope_type == "org"`. The platform-admin marker (`admin`, `org`, `*`) satisfies this. A repo-scoped admin does NOT satisfy this. List/list-deliveries deliberately open — see PENTEST-027 for the recommendation to tighten this.
- **SQL injection in new JOINs:** `o.name || '/' || r.name = $2` in `services/metadata/internal/repository/repository.go:113` is parameterised; the bound parameter is the user's `org/repo` string. Org names are constrained by `services/core/internal/service/registry.go:26` (`^[a-z0-9]+([._-][a-z0-9]+)*/[a-z0-9]+([._-][a-z0-9]+)*$`) on push and by `validateOrgName` (`services/management/internal/handler/validate.go:37`) on REST create, neither of which permits `/` — so the `o.name || '/' || r.name` predicate cannot have ambiguous matches. The metadata gRPC `GetOrCreateOrganization` (`services/metadata/internal/repository/repository.go:206`) does not itself revalidate the org name, but every caller path validates first. Worth a defence-in-depth assertion (`reOrgName`-equivalent guard inside `GetOrCreateOrganization`) but not exploitable today.
- **Info disclosure via `Org` on Repository:** the metadata cache (`services/metadata/internal/server/server.go:184-217`) keys every cacheable method on `tenant_id + ...`; `GetRepositoryByName` / `GetRepositoryByFullName` are NOT in the cache map, so the new `Repository.org` field cannot be served from a stale cross-tenant entry. The repo's `repoSelectCols` (`services/metadata/internal/repository/repository.go:53-54`) always JOINs and the tenant predicate is in every `WHERE` clause. No leak path found.
- **JSON parsing DoS on PutManifest:** covered as PENTEST-029.
- **Migration backfill DoS:** covered as PENTEST-028.
- **Postman hygiene:** covered as PENTEST-033. No real production hostnames or live bearer tokens present.

---

### PENTEST-AUTH-001 — Polymorphic api_keys cross-tenant guard (resolved pre-merge)
Closed by FE-API-048 implementation (commit `da86cdd`). `ValidateAPIKey` for service-account
keys verifies the request's claimed tenant matches `service_accounts.tenant_id`;
mismatch returns Unauthenticated + writes a `pentest.cross_tenant_attempt`
audit row. Test: T5 in spec §8.1.

### PENTEST-AUTH-002 — JWT revocation pattern extended to per-user (resolved pre-merge)
Closed by FE-API-048 implementation (commit `66aab14`). `ValidateToken` consults
`revoke:user:<user_id>` Redis key set by `SetDisabled` on a service account.
Closes the 300s JTI window for the SA disable path. Pattern is documented
under CLAUDE.md §7 "JWT Validation."

---

## Recurring Security Tasks

| Task | Frequency | Owner | Last Run |
|---|---|---|---|
| OWASP ZAP baseline scan (staging) | Weekly | — | Never |
| `govulncheck` across all repos | Every PR | CI | Every PR (all 12 service CI workflows) |
| Dependency license check | Every PR | CI | Never |
| Secret rotation review | Quarterly | — | Never |
| Audit log retention review | Quarterly | — | Never |
| GC dry-run before production schedule change | Before each change | — | Never |

---

### SEC-037 — Onboarding-flag backfill UPDATE locks every users row in one statement
- **Severity:** LOW
- **Status:** OPEN
- **Service:** `services/auth`
- **Raised:** 2026-06-27
- **Description:** `services/auth/migrations/20260629000002_users_onboarding_complete.sql:32-34` performs `UPDATE users SET onboarding_complete = true WHERE created_at < NOW()` — an unbounded single-statement UPDATE that targets every row in the table. Goose runs each migration in a single transaction so this holds a row-level lock on every users row simultaneously and a transaction-scoped UPDATE conflict with any concurrent writer. The same transaction also contains the `ALTER TABLE … ADD COLUMN`, which (although constant-default and non-rewriting on PG ≥ 11) takes an `AccessExclusiveLock` for its duration. Concurrent login traffic that touches the users table (`ResetFailedLogins`, `RecordFailedLogin`, `TouchLastLogin`, `UpdatePasswordHash`) will block until migration commit. For installs with ≤ tens of thousands of users this is sub-second and invisible; for a SaaS install with a large user table it can produce a noticeable login stall and (under saturation) lock-wait timeouts during the migration window. No data correctness issue — purely an availability concern during the deployment window.
- **Remediation:**
  1. For deployments with > ~100k humans, batch the backfill into chunks (e.g. `UPDATE users SET onboarding_complete = true WHERE id IN (SELECT id FROM users WHERE NOT onboarding_complete LIMIT 10000)` looped) and commit between batches via an out-of-band runbook, leaving the migration to only run the `ALTER TABLE … ADD COLUMN NOT NULL DEFAULT false` (which is metadata-only on PG ≥ 11).
  2. Alternatively, drop the `WHERE created_at < NOW()` backfill entirely and rely on `NOT NULL DEFAULT false` for existing rows (this would re-show the wizard to pre-existing humans — a product choice, not a security one).
  3. Document the deployment-window cost in `infra/runbooks/` so operators of large installs know to run the backfill out-of-band before the schema migration.
- **References:** CLAUDE.md §11 (migration rules — "run migrations at startup in a separate step before serving traffic"); PENTEST-028 manifest-backfill precedent (split bulk UPDATE out of migration into idempotent runbook).

### SEC-038 — `services/gc` mTLS dials lacked serverName pin and silently downgraded to plaintext on cert load error
- **Severity:** MEDIUM
- **Status:** RESOLVED
- **Service:** `services/gc`
- **Raised:** 2026-06-29
- **Description:** `services/gc/internal/server/server.go` (legacy `clientCreds(cfg)` helper, lines 315-326 of the pre-fix file) called `mtls.ClientTLSConfig(..., "")` with empty `serverName` for every outbound dial — metadata, storage, tenant — meaning the TLS handshake accepted any cert signed by the configured CA without binding the dial to a specific service identity. An attacker (or buggy operator who mis-issued certs) could pin gc to any tenant cert for the process lifetime. The same helper also fell back to `insecure.NewCredentials()` whenever `ClientTLSConfig` returned an error, so a corrupted client cert silently downgraded ALL three gRPC channels to plaintext rather than failing closed.
- **Remediation:**
  1. Drop the local helper; call `libs/auth/mtls.ClientCreds` inline at each dial site with the remote service's CN/SAN passed in (`"registry-metadata"`, `"registry-storage"`, `"registry-tenant"`).
  2. Propagate the error from `ClientCreds` on TLS load failure so startup aborts rather than silently downgrading.
- **References:** CLAUDE.md §7 (mTLS Between Services — "Client cert CN must match expected service name"), §13 (HTTP/Go hardening — fail-closed posture), CWE-295 (Improper Certificate Validation), CWE-636 (Not Failing Securely).
- **Resolved:** 2026-06-29 — commit `329c63b` on branch `fix/sec-038-gc-clientcreds` inlines `mtls.ClientCreds` per-target with non-empty `serverName` and returns the wrapped error on TLS-load failure. No insecure fallback remains on the client path; the server-side `buildGRPCOptions` plaintext-on-empty-paths branch is the documented dev fallback and remains intentionally untouched.

### SEC-039 — services/core, scanner, proxy, management client mTLS misses server-name pin and fails open on TLS load error
- **Severity:** HIGH
- **Status:** RESOLVED
- **Service:** `services/core`, `services/scanner`, `services/proxy`, `services/management`
- **Raised:** 2026-06-29
- **Description:** Same shape as SEC-038, broader blast radius. Four service entrypoints (`services/core/internal/server/server.go`, `services/scanner/internal/server/server.go`, `services/proxy/internal/server/server.go`, `services/management/internal/server/server.go`) each defined a local `clientCreds(cfg)` / `buildGRPCCreds(cfg)` helper that called `mtls.ClientTLSConfig(..., "")` — empty `serverName`, so the TLS handshake against any downstream gRPC peer did not verify the SAN/CN matched the expected service name. Combined effect: 17 dial sites total (4 in core: auth/metadata/storage/signer; 2 in scanner: metadata/storage; 2 in proxy: auth/storage; 9 in management: auth/metadata/audit/tenant/webhook/signer/scanner/gc/proxy) accepted any CA-signed cert as proof of identity. An attacker holding any leaf cert from the cluster CA could impersonate any backend service over these channels. core, scanner, and proxy additionally caught the `mtls.ClientTLSConfig` load error and fell back to `insecure.NewCredentials()` with only a `slog.Warn` — i.e. a corrupted/unreadable cert silently downgraded every downstream dial to plaintext at startup, violating CLAUDE.md §7 fail-closed posture.
- **Remediation:**
  1. Update each helper to take a `serverName` argument and delegate to `libs/auth/mtls.ClientCreds(...)`. ✅
  2. Update every dial site to pass the remote's expected CN (e.g. `"registry-auth"`, `"registry-metadata"`). ✅
  3. Drop the insecure-fallback branch on TLS load error — error propagates from `Run()`. ✅
  4. Sweep the codebase for `ClientTLSConfig(..., "")` and `ClientCreds(..., "")` to ensure no remaining empty serverName. ✅ (zero matches in production code)
- **References:** CLAUDE.md §7 ("Client cert CN must match expected service name") + fail-closed rule; CWE-295 (Improper Certificate Validation); CWE-757 (Selection of Less-Secure Algorithm); SEC-038 (same shape, gc).
- **Resolved:** 2026-06-29 — commit `41e9a72` on branch `fix/sec-039-clientcreds-sweep` (PR pending). Per-target serverName pinned at all 17 dial sites; `mtls.ClientCreds` fail-closed semantics enforced (insecure returned only when ALL cert paths empty); dev cert SANs in `scripts/gen-dev-certs.sh` line 55 already emit `DNS:registry-<svc>` for every required service so no dev-stack regression.

### SEC-048 — JWT validation fallback "try every key" path is unbounded and unmetered
- **Severity:** LOW
- **Status:** OPEN
- **Service:** `services/auth`
- **Raised:** 2026-06-30
- **Description:** `Service.ValidateToken` (services/auth/internal/service/auth.go:306) falls through to `validateWithFallback` (auth.go:983) when the JWT carries a `kid` not in the ring OR no `kid` at all. The fallback iterates every key in the ring and runs `jwt.ParseWithClaims` against each public half (~2–5 ms RSA verify per attempt). At Phase 6.5 ring sizes (1–N, expected 2–3 during a rotation window) the absolute cost is small (~6–15 ms), but the design has no upper bound: an attacker who can submit JWTs with arbitrary `kid` values forces the service to pay the full N-verify cost on every reject. Combined with the `slog.WarnContext` emitted on every successful fallback hit (auth.go:353), a flood of bogus-kid tokens also produces an unbounded warn-log volume that can mask real rotation-window signal. No counter / rate limit. Also note: on a kid-miss the keyfunc at auth.go:327 returns `s.keys.all()[0].publicKey` (the first key, deterministic since the ring is sorted by kid) — this is NOT a kid-spoof vector because `jwt.ParseWithClaims` will fail signature verify and the post-parse fallback loop re-tries the rest of the ring; an attacker cannot bypass verification, only force CPU work.
- **Remediation:**
  1. Add a `auth_jwt_fallback_total` counter labelled by reason ("kid_missing" / "kid_unknown") so the rate is visible in Prometheus.
  2. Cap the fallback search to a hard ring-size limit (e.g. reject the token outright once `len(ring) > 8`).
  3. Once every issuer stamps a kid (post-rotation grace window), add a feature flag `JWT_REJECT_MISSING_KID=true` to skip the no-kid fallback entirely (the code comment at auth.go:300 already foreshadows this).
- **References:** CLAUDE.md §13; CWE-400 (Uncontrolled Resource Consumption); CWE-307.

### SEC-049 — kid derived from PEM file base name allows silent collision on operator typo; default signer is lex-greatest not most-recent
- **Severity:** INFO
- **Status:** OPEN
- **Service:** `services/auth`
- **Raised:** 2026-06-30
- **Description:** Two related operator-ergonomics issues in `loadKeyRingFromDir` (services/auth/internal/service/keyring.go:183) and `pickDefaultSigningKID` (keyring.go:328).
  - **49a (overwrite):** `kid` is derived from the filename minus extension. `newKeyRing` rejects duplicate kids at startup, so two files with the same base name in the same dir error loudly. **However**, a realistic scenario is silent overwrite: an operator drops `2026-07-15.pem` into the ring directory not realising a key with that same base name already exists from a prior extraction; the filesystem-level replacement substitutes new key material under an unchanged kid. Validators that have cached the old JWKS will then fail to verify tokens signed by the new private half once the JWKS cache refreshes, and the rotation "works" only until the TTL expires. No fingerprint / mtime check surfaces the swap in the boot log.
  - **49b (default kid choice):** `pickDefaultSigningKID` (keyring.go:328) returns the lexicographically greatest kid when `JWT_SIGNING_KID` is empty. For operators who name keys `prod-a.pem`, `prod-b.pem`, `prod-c.pem`, the default picks `prod-c.pem` — which may not be the most recently added. The agent's comment ("timestamps or ULIDs give automatic promotion") is correct guidance, but the code does not enforce it; a freeform naming convention can sign new tokens with an old key by default.
- **Remediation:**
  1. At startup, log `(kid, sha256(publicKey)[:8])` for every key in the ring so an inadvertent overwrite shows up as a SHA change in the next boot log. Extend the existing `slog.Info("JWT key ring loaded", ...)` call at services/auth/internal/server/server.go:374.
  2. Document the naming-convention requirement in `services/auth/.env.example` next to `JWT_KEY_RING_PATH` (date-prefix or ULID, never freeform).
  3. Consider switching the default signer from "lex-greatest" to "most-recently-modified" mtime (already captured in `loaded.modTime` at keyring.go:244 but currently only used as a tie-break that never fires). Matches operator intuition ("the key I just added") regardless of naming convention.
- **References:** CLAUDE.md §7 (JWT validation, kid-targeted lookup); CWE-1188 (Insecure Default Initialization); CWE-345 (Insufficient Verification of Data Authenticity).

### SEC-053 — spec-lint rule #11 (`// audit: skip` annotation) has no allowlist; bad-faith PR can silently exempt a sensitive event
- **Severity:** LOW
- **Status:** OPEN
- **Service:** `tools/spec-lint`
- **Raised:** 2026-06-30
- **Description:** `ruleEventCatalogueCovered` (tools/spec-lint/main.go:323) treats `// audit: skip` as a self-applied opt-out: any `Routing*` constant in `libs/rabbitmq/events/events.go` either has a `case` in `services/audit/internal/eventconsumer/consumer.go`'s `mapEvent` switch, OR carries an inline / preceding-line `// audit: skip` comment. The annotation is unrestricted — a PR can add `RoutingRBACRoleGranted = "rbac.role_granted" // audit: skip` in the same diff that defines the constant, and spec-lint will PASS even though the event is meant to be audited per CLAUDE.md §10. The check enforces "every constant is decided about" but not "the decision matches the security policy."
- **Remediation:**
  1. Maintain an in-tree allowlist (e.g. `tools/spec-lint/skip_allowlist.txt`) of routing keys explicitly approved for skip; reject `// audit: skip` annotations on any constant not in the file.
  2. Alternatively, require skips to cite a CLAUDE.md or `status.md` reference in the comment (`// audit: skip — REDESIGN-001 Phase X, non-actor event`); spec-lint regexes for the reference. Cheaper than a separate file but still constrains the casual abuse vector.
  3. Track which constants currently use the annotation in the lint output so reviewers see the count drift in CI logs.
- **References:** CLAUDE.md §10 (audit trail); CWE-778 (Insufficient Logging); CWE-1173 (Improper Use of Validation Framework).

### SEC-054 — spec-lint rule #12 (mTLS validate gate) matches generic `cfg.Validate()` regex; any local Validate satisfies the rule regardless of behaviour
- **Severity:** LOW
- **Status:** OPEN
- **Service:** `tools/spec-lint`
- **Raised:** 2026-06-30
- **Description:** `ruleEveryServiceValidatesMTLS` (tools/spec-lint/main.go:392) walks every `services/<name>/cmd/server/main.go` and passes if any of `ValidateMTLSConfig`, `cfg.Validate()`, or `loader.Validate` appears in the file. The first form is precise; the latter two are not. A new service that ships its own `cfg.Validate()` checking unrelated fields (e.g. `DB_DSN != ""`) will pass the rule without ever exercising the mTLS path-validation gate that CLAUDE.md §7 requires (mTLS cert paths must be set when `OTEL_ENVIRONMENT=production`). Combined with the dev fallback in `libs/auth/mtls` that downgrades to `insecure.NewCredentials()` when paths are unset, this lets a regression ship where a production service silently runs plaintext gRPC because nothing forced the mTLS validator to run.
- **Remediation:**
  1. Tighten the gate regex to require an actual reference to the mTLS validator: `ValidateMTLSConfig|loader\.ValidateMTLSConfig|mtls\.Validate`.
  2. If services must keep a local `cfg.Validate()` indirection, additionally lint that the local Validate body itself calls `loader.ValidateMTLSConfig` (one-level callgraph walk via `go/ast` rather than text grep).
  3. Add a dedicated runtime assertion in `libs/auth/mtls` that panics on `Bootstrap` if `OTEL_ENVIRONMENT=production` AND cert paths are empty — defence in depth so the lint regression is caught at startup even if spec-lint misses it.
- **References:** CLAUDE.md §7 (mTLS between services, production validation requirement); CWE-311 (Missing Encryption); CWE-358 (Improperly Implemented Security Check).

### SEC-055 — `sanitiseSAName` strips only 4 chars; relies on upstream regex for shell/YAML safety
- **Severity:** LOW
- **Status:** OPEN
- **Service:** `frontend/`
- **Raised:** 2026-06-30
- **Description:** `frontend/src/lib/credential-snippets.ts:32` `sanitiseSAName` removes `["\`$\\]` from the service-account name before splicing it into four copy-paste-ready snippets. Three of those splice sites are NOT inside a properly-quoted context: `--username ${safe}` (docker login, unquoted bash arg), `--docker-username=${safe}` (kubectl, unquoted bash flag), and `username: ${safe}` (GHA YAML flow scalar). The remaining sanitizer set (`safe` contains everything except `"`, `` ` ``, `$`, `\`) still admits semicolons, pipes, ampersands, parens, redirections (`<>`), globs (`*?[`), comments (`#`), whitespace, and newlines — any of which would break out of the intended quoting if they ever appeared in a SA name. Today this is safe **only because** the server-side create-time regex `^[a-z0-9]+([._-][a-z0-9]+)*$` (services/auth/internal/handler/http_service_accounts.go:31) admits no shell metacharacters. Comment in the source already describes the function as "defence in depth on top of the SA-name regex enforced at create time" — accurate but fragile: if the server-side regex is ever loosened (e.g. to support uppercase, unicode, spaces, mixed punctuation), the FE silently regresses with no test guarding the invariant.
- **Remediation:**
  1. Tighten the sanitizer to whitelist (`replace(/[^a-z0-9._-]/g, "")`) rather than blacklist — encodes the same contract the server enforces, fails closed on any future server-side relaxation.
  2. Add a `buildSnippets` test asserting that a SA name containing `;`, `|`, `&`, `\n`, ` ` is either stripped or rejected — the current "escapes special characters" test only covers `"`.
  3. Optional: surround the SA name in single quotes in the docker/kubectl shell snippets (`--username '${safe}'`) — single quotes don't undergo any expansion, so even an unsanitised name can't break out (except via embedded `'`). Belt-and-braces.
- **References:** CLAUDE.md §7 (input validation, allowlist over blocklist); CLAUDE.md §13 (no `exec.Command` with user input — analogue here is "snippet handed to operator's shell"); CWE-78 (OS Command Injection); CWE-116 (Improper Encoding/Escaping of Output); CWE-1287 (Improper Validation of Specified Type of Input).

### SEC-056 — Stale test docstring contradicts auth-gating on `/api/v1/registry-info`
- **Severity:** LOW
- **Status:** OPEN
- **Service:** `services/management`
- **Raised:** 2026-06-30
- **Description:** `services/management/internal/handler/registry_info_test.go:11-13` docstring reads "The endpoint is unauthenticated by design (parallels handleDeploymentInfo) and leaks no tenant data — just the deployment's registry hostname." This is **contradicted by the production wiring** at `services/management/internal/handler/handler.go:302` which wraps the route in `authMW(http.HandlerFunc(h.handleRegistryInfo))`. The handler source comment (`registry_info.go:13-16`) correctly notes the route IS auth-gated because the matching FE page lives behind `/api-keys/helpers`. Today this is a documentation defect, not a code defect — the test exercises the bare handler function so it cannot actually verify the auth wrapping, and the discrepancy is invisible at runtime. The risk is that a future reviewer takes the test docstring at face value and "fixes" the route by removing `authMW` to align with the stated intent, silently exposing the endpoint to anonymous callers. Hostname disclosure to anon callers is low impact (the registry host is publicly resolvable by definition), but the larger structural issue is the test cannot fail when auth is dropped.
- **Remediation:**
  1. Rewrite the docstring to match reality: "The endpoint is auth-gated by `authMW` at the route layer (handler.go:302). This test exercises the bare handler function; auth wiring is covered by `TestRegister*` smoke tests."
  2. Add a smoke assertion in the existing handler registration test (or a new one) that `GET /api/v1/registry-info` without a Bearer token returns 401 — pins the auth-gating against accidental removal.
- **References:** CLAUDE.md §7 (auth requirements); CLAUDE.md §13 (workflow gate — test must reflect production wiring); CWE-1059 (Insufficient Technical Documentation); CWE-862 (Missing Authorization — risk shape, not present today).

### SEC-071 — `rotate-kek --verify` takes unnecessary `FOR UPDATE` row locks
- **Severity:** LOW
- **Status:** OPEN
- **Service:** `libs/crypto/rekey`
- **Raised:** 2026-07-03
- **Description:** `verifyTable` (`libs/crypto/rekey/sweep.go:256`) reuses `selectSQL(spec)`, whose SQL ends in `... FOR UPDATE` (`sweep.go:132`). Verify mode is documented as "never mutates" (`sweep.go:55-57`) and runs the query on the pool (`pool.Query`) in an implicit transaction, but `FOR UPDATE` still acquires row-level write locks on every candidate row for the duration of the scan. If an operator runs `--verify` against a live database (the runbook stops the service for `--rotate`, but `--verify` is the natural pre/post-flight check an operator would run while the service is up), the verify scan can briefly block concurrent writers to the credential tables (e.g. an SSO-config update, an upstream-registry credential rotation). No data-integrity or confidentiality impact — purely an availability/lock-contention nit. `--rotate`/`--dry-run` correctly need `FOR UPDATE` (they select-then-update in one tx); verify does not.
- **Remediation:**
  1. Give verify its own lock-free select — e.g. add a `forUpdate bool` param to `selectSQL` (or a sibling `selectReadSQL`) that omits the `FOR UPDATE` suffix, and call it from `verifyTable`.
  2. Add a regression test asserting the verify path issues no `FOR UPDATE` (string-match the generated SQL, or a concurrency test showing verify does not block a writer).
- **References:** CLAUDE.md §11 (DB conventions); CWE-667 (Improper Locking).

### SEC-072 — `rotate-kek --generate` prints the fresh KEK to stdout (scrollback / CI-log capture)
- **Severity:** INFO
- **Status:** OPEN
- **Service:** `libs/crypto/rekey`
- **Raised:** 2026-07-03
- **Description:** `RunCLI` (`libs/crypto/rekey/cli.go:58-65`) mints a fresh 32-byte KEK with `GenerateKeyHex` and writes it in cleartext to stdout via `fmt.Fprintln(stdout, h)`. This is the intended UX (the operator must capture the new key to paste into the secrets manager), and the key never touches slog/error paths — but a hex KEK on stdout lands in terminal scrollback, shell session recordings, and, critically, CI/job logs if `--generate` is ever wired into an automation step. There is no accompanying "do not run this in CI / clear your scrollback" caveat at the call site. Not a code defect; a handling-guidance gap for the highest-value secret in the platform.
- **Remediation:**
  1. Add a stderr warning line alongside the printed key (e.g. "WARNING: this is the new KEK in cleartext — capture it into your secrets manager and clear your scrollback; never run --generate in CI logs").
  2. Confirm `infra/runbooks/kek-rotation.md` documents that `--generate` must be run on an operator workstation, not in a pipeline, and that the value is redacted from any recorded session.
- **References:** CLAUDE.md §7 (secrets never logged / handled carelessly); CWE-532 (Insertion of Sensitive Information into Log File).

### SEC-073 — No guard against `KEK_OLD_HEX == KEK_NEW_HEX` (silent no-op rotation → false confidence)
- **Severity:** INFO
- **Status:** OPEN
- **Service:** `libs/crypto/rekey`
- **Raised:** 2026-07-03
- **Description:** `RunCLI` parses both keys (`cli.go:70-80`) but never checks that they differ. If an operator misconfigures the environment so `KEK_OLD_HEX == KEK_NEW_HEX` (e.g. a copy-paste error, or both point at the same secrets-manager entry), the rotate sweep succeeds — every row decrypts and re-encrypts under the same key (with a fresh nonce), rows get stamped with the next `kek_version`, and `--verify` reports zero rows remaining. The operator receives a clean success and a bumped version generation while the compromised/retired key is still the live key. This defeats the security objective of the rotation (moving off a key believed exposed) with no signal. Length+hex validation catches malformed keys but not this equality case.
- **Remediation:**
  1. In `RunCLI`, after parsing both keys for rotate/dry-run, reject with a `ValidationError` when `subtle.ConstantTimeCompare(oldKey, newKey) == 1` (constant-time to avoid a timing oracle on the byte comparison).
  2. Note the check in the runbook's pre-flight section.
- **References:** CLAUDE.md §7 (key rotation intent); CWE-323 (Reusing a Nonce/Key Pair — shape: key reuse across a rotation boundary); CWE-665 (Improper Initialization).

### SEC-074 — Plaintext secret buffer not zeroed after re-encryption (defense-in-depth)
- **Severity:** INFO
- **Status:** OPEN
- **Service:** `libs/crypto/rekey`
- **Raised:** 2026-07-03
- **Description:** `Rekey` (`libs/crypto/rekey/rekey.go:28-38`) decrypts to a `plain []byte`, immediately re-encrypts it, and returns without wiping `plain`. During a full sweep every credential in the table is briefly resident in cleartext on the heap and lingers until GC, so a core dump / swap / memory-scraping attacker on the operator host during rotation could recover secrets. This is consistent with the existing posture of `libs/crypto/aes` (which also does not zero its plaintext buffers), so it is not a regression introduced by this PR — recording it as the natural place to add best-effort zeroization for the one tool whose entire job is to touch every secret at once. Go's GC makes true guaranteed wiping hard, so treat as best-effort hardening, not a blocker.
- **Remediation:**
  1. Best-effort: after `aes.Encrypt` returns in `Rekey`, `for i := range plain { plain[i] = 0 }` before returning. Consider the same in `libs/crypto/aes.Decrypt` callers that handle long-lived secrets.
  2. Document the residual-memory caveat in the runbook (run rotation on a host with swap disabled / encrypted swap, restrict core dumps).
- **References:** CLAUDE.md §7 (secrets hygiene); CWE-316 (Cleartext Storage of Sensitive Information in Memory); CWE-226 (Sensitive Information in Resource Not Removed Before Reuse).

### SEC-075 — Username/password login path uses kind-agnostic `GetByUsername`; guard relies solely on empty SA password_hash
- **Severity:** INFO
- **Status:** OPEN
- **Service:** `services/auth`
- **Raised:** 2026-07-03
- **Description:** Surfaced while reviewing PR #250's removal of the `lint-user-queries.sh` kind-guard CI check. The human password-login path `AuthenticateUser` (`services/auth/internal/service/auth.go:1080`) resolves the account via the kind-agnostic `UserRepository.GetByUsername` (`services/auth/internal/repository/user.go:147`, whose SQL carries an `-- allow-any-kind` annotation), NOT through a kind='human' guarded helper. The `GetHumanByUsername` helper that the `GetByUsername` docstring (`user.go:144,154`) and the now-deleted lint script both name as the required login-path variant **does not actually exist** in the repository. As a result the FE-API-048 §4.1 kind guard is not applied on the username/password path — a service-account shadow row (`kind='service_account'`, synthetic username `sa-<8hex>`) can be returned by the lookup. This is NOT exploitable today: SA shadow rows are inserted with `password_hash=''` (`services/auth/internal/repository/service_account.go:144`) and `argon2.Verify` rejects an empty hash, so no password can authenticate an SA row (confirmed by the fixture note at `services/auth/internal/testutil/sa_fixtures.go:145`). The exposure is therefore defense-in-depth only: the single barrier is the empty-hash invariant rather than the kind guard the contract advertises. Pre-existing on `main`; not introduced by PR #250. Removing the CI lint does not change this (the lint passed `GetByUsername` via its `-- allow-any-kind` annotation and never checked caller routing), which is why PR #250 is assessed as no-regression.
- **Remediation:**
  1. Add a `GetHumanByID`-style `GetHumanByUsername(ctx, tenantID, username)` helper enforcing `AND kind = 'human'` at the SQL layer, and switch `AuthenticateUser` to call it so the kind guard becomes the primary control on the password path (matching the docstring/contract).
  2. Alternatively, if `GetByUsername` must stay kind-agnostic for its other callers, add an explicit `kind == "human"` check in `AuthenticateUser` after the lookup and return `ErrInvalidCredentials` for SA rows.
  3. Fix the misleading `GetByUsername` docstring (`user.go:144,154`) which references a non-existent `GetHumanByUsername`, so the contract and code agree.
- **References:** FE-API-048 §4.1 (kind-guard contract); CLAUDE.md §7 (input validation / auth); CWE-287 (Improper Authentication); CWE-1188 (defense-in-depth on the auth path relying on a single invariant).

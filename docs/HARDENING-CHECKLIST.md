# Security Hardening Checklist

> **What this file is:** the per-service hardening checklist that every Go
> service must satisfy. Originally lived in `CLAUDE.md` §13; extracted
> 2026-06-30 to keep the project rules file lean.
>
> **What this file is NOT:** the audit log. Per-CVE lifecycle (SEC-NNN
> IDs + resolution dates) lives in [`../security.md`](../security.md).
> Currently-open security items are tracked in
> [`../status-tracker.md`](../status-tracker.md).
>
> **Scope:** these rules apply to **every service** without exception.
> A new service is not "done" until every box can be ticked or the
> exception is documented with a SEC-NNN reference.

---

## Go code

- [ ] No `unsafe` package usage without a documented, reviewed justification
- [ ] No `exec.Command` with any part of user-supplied input
- [ ] No `os.Getenv` for secrets inside handlers — load at startup into a typed config struct
- [ ] All file paths sanitised with `filepath.Clean` and checked against an allowed prefix
- [ ] HTTP clients: always set timeouts (`Timeout`, `TLSHandshakeTimeout`, `ResponseHeaderTimeout`)
- [ ] No default HTTP client (`http.DefaultClient`) — always create a configured client
- [ ] `context.Background()` never used inside request handlers — always propagate request context (SEC-028)
- [ ] Randomness: use `crypto/rand`, never `math/rand` for security-sensitive values

## HTTP

- [ ] `Content-Security-Policy` header on all HTML responses (via `libs/middleware/http.SecureHeaders`)
- [ ] `X-Content-Type-Options: nosniff` on all responses
- [ ] `X-Frame-Options: DENY` on all responses
- [ ] HSTS on all HTTPS responses
- [ ] No sensitive data in URL query parameters (use POST body or headers)
- [ ] CORS: explicitly configured allowlist, never `*`
- [ ] Request body size limits set on all HTTP servers
- [ ] `ReadHeaderTimeout: 10s` (Slowloris protection), `ReadTimeout` + `WriteTimeout` set per service (SEC-019/020)

## Dependencies

- [ ] `govulncheck` run in CI on every PR
- [ ] `go mod verify` run in CI
- [ ] Dependabot or Renovate configured for automated dependency PRs
- [ ] No indirect dependency pinned in `go.mod` without a comment explaining why
- [ ] License check in CI (reject GPL/AGPL dependencies unless reviewed)

## Secrets

- [ ] No secrets in Git history (pre-commit hook: `gitleaks`)
- [ ] No secrets in Docker image layers (`docker history` checked in CI)
- [ ] No secrets in Helm values files
- [ ] Secret rotation procedure documented in `infra/runbooks/secret-rotation.md`

---

> **Numbered SEC items** (SEC-001..SEC-036 and beyond) and their
> resolution notes live in [`../security.md`](../security.md).

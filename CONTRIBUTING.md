# Contributing to OCI-Janus

Thanks for considering a contribution. This is a small project run by a
small team — contributions of any size are genuinely valuable.

By submitting a contribution, you agree that it will be licensed under
the [Apache License 2.0](LICENSE) that covers the rest of the project.

---

## Quick links

- **Found a bug?** Open a [GitHub Issue](https://github.com/steveokay/oci-janus/issues/new) using the bug template.
- **Want to discuss a feature?** Open a [Discussion](https://github.com/steveokay/oci-janus/discussions) or an Issue with the `enhancement` label.
- **Security issue?** Don't open a public Issue. Use [private vulnerability reporting](https://github.com/steveokay/oci-janus/security/advisories/new). Full policy in [`.github/SECURITY.md`](.github/SECURITY.md); past resolutions in [`security.md`](security.md).
- **Project conduct:** see [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md). The short version: be kind, be respectful, focus on what's best for the community.
- **Want to chat?** Comment on an existing Discussion or Issue. There isn't a Discord yet; if usage warrants it, we'll add one.

---

## Before you start

A few things to know:

1. **The codebase is opinionated.** [`CLAUDE.md`](CLAUDE.md) is the canonical reference for architecture decisions, security rules, and coding conventions. Read it before you write code — it'll save both of us time in review.
2. **Bug fixes are easier to land than features.** If you have a feature idea, open a Discussion first so we can talk about whether it fits the platform's direction (see [`futures.md`](futures.md) for the existing backlog).
3. **One change per PR.** Mixed-concern PRs are hard to review and risky to revert. Split refactors out from feature work.
4. **Tests are not optional.** New code needs unit tests; behaviour-changing fixes need a regression test that reproduces the bug.

---

## Setting up your dev environment

See [Quick Start](README.md#quick-start-local-dev) in the README for the canonical setup. The short version:

```bash
# Clone + workspace setup
git clone https://github.com/steveokay/oci-janus.git
cd oci-janus
go work sync

# Start the dev stack
cd infra/docker-compose
cp .env.example .env       # edit secrets
docker compose up -d

# Run the frontend dev server
cd ../../frontend
npm install
npm run dev                # http://localhost:5173
```

Dev login: `admin` / `Admin1234!dev`, tenant `98dbe36b-ef28-4903-b25c-bff1b2921c9e`.

---

## Picking something to work on

| Label / source | What it means |
|---|---|
| `good first issue` (when we have them) | Small, well-scoped, no deep architecture knowledge required |
| Open items in [`status-tracker.md`](status-tracker.md) | Active remediation work — known bugs / open `REM-*` / `PENTEST-*` items |
| [`futures.md`](futures.md) | Prioritised backlog. Tier 1 is "needed for production"; Tier 2 / Tier 3 are operationally valuable / nice-to-have |
| Issues you've reported yourself | If you spotted it, you're probably best placed to fix it |

If you're not sure whether something is in scope, ask first. We'd rather spend 5 minutes on a "yes / no / here's why" conversation than 5 hours reviewing work that doesn't fit.

---

## Code style + conventions

### Backend (Go)

- **Go 1.23+** (toolchain pinned at `go 1.25.7` in `go.work`).
- **No ORM.** All SQL via `pgx/v5` directly. Parameterised queries only.
- **One service per `services/<name>/`** with the standard layout (`cmd/server`, `internal/{config,server,handler,service,repository,middleware}`, `migrations`, `Dockerfile`).
- **No business logic in `libs/`.** Shared utilities only.
- **Errors:** return them; don't swallow them. Use `errors.Is` / `errors.As` for matching. `slog` for structured logging.
- **Migrations:** `pressly/goose` format, naming `YYYYMMDDHHMMSS_<description>.sql`. Reversible (`-- +goose Down` block required). Never drop a column without a deprecation cycle.
- **gRPC over mTLS** for service-to-service. New RPCs go in `proto/<service>/v1/<service>.proto`; regenerate with `make proto`.
- **Tests** with the standard library `testing` package. Integration tests behind `//go:build integration` using `testcontainers-go`.

### Frontend (React / TypeScript)

- **Vite + TanStack Router** (file-based, see `frontend/src/routes/`).
- **TanStack Query** for all data fetching. Mutations invalidate keys explicitly; never use `refetch()` for write paths.
- **react-hook-form + zod** for forms.
- **Tailwind v4** (CSS variables in `frontend/src/index.css`).
- **Sonner** for toasts. Lucide for icons.
- **No `any`.** No `// @ts-ignore`. `npx tsc --noEmit` must pass.

### Comments

- **Default to writing no comments.** Code with good names explains itself.
- **Write a comment when the WHY is non-obvious** — a hidden constraint, a subtle invariant, a workaround for a specific bug, behavior that would surprise a reader.
- **Don't explain WHAT the code does** — that's the code's job.
- **Don't reference the current task** ("fixes issue #123", "added for the Y flow") — those belong in the PR description, not the source.

### Commits

- **Conventional-ish commits** without strict enforcement. Format: `<type>(<scope>): <subject>`. Examples:
  - `feat(audit): add SIEM streaming Phase 2 with DLX drain`
  - `fix(tenant): VerifyDomainNow returns verified=false for pending DNS`
  - `docs(status): track REM-016 — MapDBError pg-code mapping`
- **One logical change per commit.** Easier to revert, easier to review.
- **Co-Authored-By footer** if you pair-programmed or got significant AI assistance.

---

## Workflow

1. **Fork the repo** to your GitHub account. (Or, if you have direct push access, work on a branch.)
2. **Create a feature branch** from `main`:
   ```bash
   git checkout -b feat/your-feature
   # or  fix/your-bug, docs/your-update
   ```
3. **Make your changes.** Run tests locally:
   ```bash
   # Backend
   cd services/<name> && go test ./...
   make lint                # optional but recommended

   # Frontend
   cd frontend && npx tsc --noEmit && npm run test
   ```
4. **Push** to your fork / branch and **open a PR** against `main`.
5. **PR description** should cover:
   - What you changed and why
   - Test plan (what you ran, what you verified)
   - Any caveats / follow-ups
   - Screenshots for UI changes
   - Links to related issues / discussions
6. **Wait for review.** Be patient — this is a small project. Address feedback by pushing new commits (don't force-push during review unless you're rebasing on `main`).
7. **Once approved**, the maintainer will merge.

---

## What gets accepted

| ✅ Likely accepted | ⚠️ Discuss first | ❌ Unlikely |
|---|---|---|
| Bug fixes with regression tests | New top-level features | Cosmetic refactors without behaviour change |
| Docs improvements | Significant architecture changes | Removing existing functionality |
| Test coverage for under-tested code | New service dependencies (databases, queues, etc.) | Anything that breaks the docker-compose dev stack |
| Performance optimisations with benchmarks | New build dependencies | Replacements of stable libraries with competitors |
| Security hardening | Major UI redesigns | Personal preferences (style, formatting) without consensus |
| Better error messages | Plan-tier / billing logic | |

---

## Reporting security issues

**Do not open a public Issue for security vulnerabilities.** Instead:

1. Use GitHub's [private vulnerability reporting](https://github.com/steveokay/oci-janus/security/advisories/new)
2. Or email the maintainer directly (see profile)

Include reproduction steps, affected versions, and impact assessment. We'll acknowledge within 72 hours and work with you on disclosure timing.

The platform's security tracker is [`security.md`](security.md) — every issue gets a `SEC-NNN` or `PENTEST-NNN` ID and a resolution note. Most past audits are documented there.

---

## License

Contributions are licensed under [Apache License 2.0](LICENSE). The same license that covers the rest of the project. No CLA required; the Apache 2.0 inbound contribution clause is sufficient.

If you're contributing on behalf of an employer, please make sure your employer is comfortable with that — Apache 2.0 contributions typically need internal approval at larger companies.

---

## Sponsoring the project

If your company uses OCI-Janus and wants to support its development, consider [sponsoring on GitHub](https://github.com/sponsors/steveokay). It directly funds maintenance time and helps prioritise community-requested features.

---

> **Questions?** Open a [Discussion](https://github.com/steveokay/oci-janus/discussions) or comment on an existing one. We're happy to talk before you write code.

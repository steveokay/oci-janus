# spec-lint

CI-runnable check that the claims in `CLAUDE.md` still match the codebase.
The idea: when an architecture or security claim drifts from reality, a
build fails rather than the drift surviving until the next review batch.

REDESIGN-001 Phase 7.3.

## Usage

```bash
# From repo root:
go -C tools/spec-lint build -o spec-lint
./tools/spec-lint/spec-lint .
```

Exit code `0` = every rule passes. Exit code `1` = at least one rule
flagged a drift. CI runs the same invocation; see
`.github/workflows/ci-spec-lint.yml`.

## Adding a rule

Each rule is one entry in the `rules` slice in `main.go`:

```go
{
    Description: "CLAUDE.md §X 'load-bearing claim text' — what the code must look like",
    Check:       ruleSomething,
}
```

The `Check` function takes the repo root and returns `nil` on pass or an
error describing the drift on fail. Helpers (`readFile`, `grepFile`,
`grepGlob`) are in `main.go`.

Keep rules:

- **Small.** One claim per rule. If a rule is checking three things, split it.
- **Fast.** Each rule should complete in milliseconds — just file reads.
- **Specific.** The diagnostic message should name the file, the symbol,
  and the CLAUDE.md section. The point is to make the fix obvious.
- **Stable.** Rules should fail only when reality drifts, not when an
  irrelevant whitespace change happens.

## Current rule catalogue

| # | Claim | Evidence |
|---|---|---|
| 1 | §7 mTLS hot reload | `libs/auth/mtls/mtls.go` references `GetCertificate` or `GetClientCertificate` |
| 2 | §7 peer-CN allowlist | `libs/middleware/grpc/peer_cn.go` exports `PeerCNAllowlist` + `PeerCNAllowlistFromEnv` |
| 3 | §7 + Decision #15 audit FORCE RLS | A migration under `services/audit/migrations/` declares `FORCE ROW LEVEL SECURITY` |
| 4 | §7 + Decision #30 no audit_chain_tip | No migration `CREATE TABLE` references `audit_chain_tip` (SEC-050 BLOCKER guard) |
| 5 | §10 + Decision #30 audit chain_seq | A migration declares `chain_seq BIGINT GENERATED ALWAYS AS IDENTITY` + `repository.go` queries `ORDER BY chain_seq DESC` |
| 6 | §7 + Decision #26 is_global_admin | A migration under `services/auth/migrations/` mentions `is_global_admin` |
| 7 | §1 + Decision #28 bootstrap_tenant_id | A migration under `services/tenant/migrations/` mentions `deployment_metadata` or `bootstrap_tenant_id` |
| 8 | §7 + Phase 6.5 ring size cap | `services/auth/internal/service/keyring.go` declares `maxKeyRingSize` |
| 9 | §7 + Decision #29 AES Version byte | `libs/crypto/aes/aes.go` declares `Version byte = 0x01` |
| 10 | §1 RM-001 custom domains removed | A migration under `services/tenant/migrations/` `DROP TABLE`s `tenant_domains` |
| 11 | §10 Audit Trail catalogue closed | Every `Routing*` constant in `libs/rabbitmq/events/events.go` either appears in `services/audit/internal/eventconsumer/consumer.go` or carries a `// audit: skip` annotation |
| 12 | §7 Phase 1.3 mTLS validation | Every `services/*/cmd/server/main.go` calls `ValidateMTLSConfig` or `cfg.Validate()` |
| 13 | §10 metrics exist | Every metric named in CLAUDE.md §10 is registered in `libs/observability/metrics/metrics.go` |

## When a rule fails

The fix is *one of*:

1. **Code drifted from spec.** Restore the invariant in code (the usual
   case). Don't silence the rule.
2. **Spec drifted from code.** The claim in CLAUDE.md is genuinely
   obsolete. Update CLAUDE.md AND update the matching rule (or delete
   it). Both edits in the same PR.
3. **Rule is wrong.** Genuine bugs in the check function. Fix the rule;
   don't change the production code or CLAUDE.md to work around it.

If you find yourself reaching for option 1.5 ("silence the rule for this
PR, fix it next sprint"), the rule was right and the work isn't done.

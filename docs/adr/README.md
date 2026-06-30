# Architecture Decision Records

This directory holds one ADR per entry in [CLAUDE.md §14 Decision Log](../../CLAUDE.md#14-decision-log). Each ADR carries a load-bearing `Verified by` pointer to a file:symbol that, if removed, would invalidate the decision — making the ADRs grep-able from the codebase.

Status legend:

- **ACCEPTED** — current, in force.
- **SUPERSEDED** — replaced by a later ADR; kept for historical context with both legacy and current pointers.

| ADR | Title | Status | Date |
|---|---|---|---|
| [ADR-0001](0001-grpc-sync-rabbitmq-async.md) | gRPC for sync, RabbitMQ for async | ACCEPTED | Initial |
| [ADR-0002](0002-rabbitmq-over-kafka.md) | RabbitMQ over Kafka | ACCEPTED | Initial |
| [ADR-0003](0003-jwt-apikey-mtls-defence-in-depth.md) | JWT RS256 + API keys + mTLS | ACCEPTED | Initial |
| [ADR-0004](0004-multi-tenant-with-custom-domains.md) | Multi-tenant with custom domains | SUPERSEDED by ADR-0025 | Initial |
| [ADR-0005](0005-pluggable-scanner-external-process.md) | Pluggable scanner interface (external process only) | ACCEPTED | 2026-06-09 |
| [ADR-0006](0006-cosign-and-notary-v2.md) | Cosign + Notary v2 | ACCEPTED | Initial |
| [ADR-0007](0007-otel-pluggable-exporter.md) | OTEL with pluggable exporter | ACCEPTED | Initial |
| [ADR-0008](0008-monorepo-with-go-workspaces.md) | Monorepo with Go workspaces | ACCEPTED | 2026-06-09 |
| [ADR-0009](0009-pgx-raw-sql-no-orm.md) | pgx/v5 with raw SQL, no ORM | ACCEPTED | Initial |
| [ADR-0010](0010-distroless-final-docker-image.md) | Distroless final Docker image | ACCEPTED | Initial |
| [ADR-0011](0011-no-presigned-urls-to-clients.md) | No presigned URLs to clients | ACCEPTED | Initial |
| [ADR-0012](0012-postgresql-rls-as-second-layer.md) | PostgreSQL RLS as second layer | ACCEPTED | Initial |
| [ADR-0013](0013-trivy-default-scanner-plugin.md) | Trivy as default scanner plugin | ACCEPTED | 2026-06-09 |
| [ADR-0014](0014-vault-dev-mode-as-local-kms.md) | Vault dev mode as local KMS | ACCEPTED | 2026-06-09 |
| [ADR-0015](0015-audit-force-rls-low-privilege-role.md) | Audit FORCE RLS + `registry_audit_app` role | ACCEPTED | 2026-06-09 |
| [ADR-0016](0016-gc-advisory-locks-fnv64a.md) | GC advisory locks via `pg_try_advisory_lock` | ACCEPTED | 2026-06-09 |
| [ADR-0017](0017-scanner-owns-its-database.md) | `services/scanner` owns its own DB | ACCEPTED | 2026-06-20 |
| [ADR-0018](0018-gc-owns-its-database.md) | `services/gc` owns its own DB | ACCEPTED | 2026-06-21 |
| [ADR-0019](0019-per-tenant-sso-model.md) | Per-tenant SSO model (OAuth + SAML) | SUPERSEDED by ADR-0027 | 2026-06-21 |
| [ADR-0020](0020-custom-domain-primary-mutex.md) | Custom-domain primary mutex on `is_primary` | SUPERSEDED by ADR-0025 | 2026-06-20 |
| [ADR-0021](0021-generic-getanalytics-audit-rpc.md) | Generic `GetAnalytics` RPC over `services/audit` | ACCEPTED | 2026-06-21 |
| [ADR-0022](0022-service-account-shadow-users.md) | Service-account principals as shadow users | ACCEPTED | 2026-06-22 |
| [ADR-0023](0023-two-layer-tag-immutability.md) | Two-layer tag immutability (repo + per-tag) | ACCEPTED | 2026-06-23 |
| [ADR-0024](0024-unified-bearer-dispatch.md) | Unified Bearer dispatch (JWT + `key.` prefix) | ACCEPTED | 2026-06-23 |
| [ADR-0025](0025-single-tenant-default-deployment-mode.md) | Self-hosted single-tenant by default | ACCEPTED | 2026-06-26 |
| [ADR-0026](0026-is-global-admin-typed-primitive.md) | `users.is_global_admin` typed primitive | ACCEPTED | 2026-06-28 |
| [ADR-0027](0027-global-sso-config.md) | Global SSO config replaces per-tenant `auth_providers` | ACCEPTED | 2026-06-28 |
| [ADR-0028](0028-bootstrap-cli-replaces-dev-seed.md) | Bootstrap CLI replaces dev-seed admin migration | ACCEPTED | 2026-06-27 |
| [ADR-0029](0029-aes-gcm-version-byte-prefix.md) | AES-256-GCM ciphertext `Version = 0x01` prefix | ACCEPTED | 2026-06-30 |
| [ADR-0030](0030-audit-hash-chain-tip-from-chain-seq.md) | Audit hash-chain tip derived from `chain_seq` | ACCEPTED | 2026-06-30 |

## Supersession pairs

| Old | → | New | Reason |
|---|---|---|---|
| ADR-0004 (Multi-tenant custom domains) | → | ADR-0025 (Single-tenant default) | REDESIGN-001 Phase 0 — custom-domain surface removed (RM-001). |
| ADR-0019 (Per-tenant SSO) | → | ADR-0027 (Global SSO) | REDESIGN-001 Phase 2.2 — self-hosters have one IdP. |
| ADR-0020 (Domain `is_primary` mutex) | → | ADR-0025 | Removed alongside the custom-domain table (RM-001). |

## How ADRs are added

When CLAUDE.md §14 gains a new row, add a matching `NNNN-<slug>.md` file using the template inside ADR-0030 as a model (≤30 lines, with a real `Verified by` file:symbol pointer). When a decision is reversed, mark the old ADR `SUPERSEDED by ADR-NNNN`, keep both legacy + current `Verified by` pointers, and add a row to the supersession table above.

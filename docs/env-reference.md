# Environment variable reference

Every configuration variable for all 14 services, consolidated from each
service's `.env.example` file. These files are the source of truth; this
page is **generated** from them (`libs/cmd/env-ref-gen`) and a CI
drift-guard keeps it in sync — so it never falls behind the code.

!!! warning "Secrets come from the environment"
    The **Example / default** column shows the placeholder from `.env.example`.
    An **empty** value means the variable has no default and must be supplied
    (secrets always do). Never commit real secrets — inject them from a secrets
    manager. See [Self-hosting](SELF-HOSTING.md) for the KEK inventory and
    production guidance.

!!! note "Regenerating"
    ```bash
    cd libs && go run ./cmd/env-ref-gen   # writes ../docs/env-reference.md
    ```

## registry-audit

`services/audit/.env.example`

### Server

| Variable | Example / default | Description |
|---|---|---|
| `GRPC_ADDR` | `:50051` | — |
| `HTTP_ADDR` | `:8080` | — |
| `LOG_LEVEL` | `info` | — |
| `LOG_FORMAT` | `json` | — |

### mTLS

| Variable | Example / default | Description |
|---|---|---|
| `MTLS_REQUIRED` | `true` | — |
| `MTLS_CA_CERT_PATH` | `/certs/ca.crt` | — |
| `MTLS_CERT_PATH` | `/certs/audit.crt` | — |
| `MTLS_KEY_PATH` | `/certs/audit.key` | — |

### Observability

| Variable | Example / default | Description |
|---|---|---|
| `OTEL_EXPORTER` | `stdout` | — |
| `OTEL_ENDPOINT` | — | — |
| `OTEL_SERVICE_NAME` | `registry-audit` | — |
| `OTEL_ENVIRONMENT` | `development` | — |

### Database

| Variable | Example / default | Description |
|---|---|---|
| `DB_DSN` | `postgres://audit:secret@postgres:5432/registry_audit?sslmode=require` | — |
| `DB_MAX_CONNS` | `20` | — |

### RabbitMQ

| Variable | Example / default | Description |
|---|---|---|
| `RABBITMQ_URL` | `amqp://guest:guest@rabbitmq:5672/` | — |

### Email notification channel

| Variable | Example / default | Description |
|---|---|---|
| `NOTIFY_EMAIL_KEY_HEX` | — | 32-byte AES-256-GCM KEK (64 hex chars) sealing email_transport_config secrets (resend_api_key / smtp_password). UNSET disables the email channel entirely: the transport RPCs return FAILED_PRECONDITION and the send loop idles. SET-but-not-64-hex-chars fails closed at startup (a bad KEK would silently corrupt secrets). Swept by rotate-kek (RED-FU-015). |
| `AUTH_GRPC_ADDR` | — | mTLS target for registry-auth.ResolveUserEmails — the dispatcher resolves email recipients through it. Empty disables email fan-out (bell channel unaffected). |
| `PLATFORM_HOST` | — | Optional public base URL used to build absolute CTA links in emails (e.g. https://registry.example.com). Empty → email links are app-relative. |
| `NOTIFY_WEBHOOK_KEY_HEX` | — | FUT-019 webhook notification channel — 32-byte hex (64 chars) AES-256-GCM KEK sealing the org webhook HMAC secret. Unset disables the webhook channel. |

### Retention

| Variable | Example / default | Description |
|---|---|---|
| `AUDIT_RETENTION_DAYS` | `365` | How many days to keep audit events (default: 365) |

### Trusted gateway

| Variable | Example / default | Description |
|---|---|---|
| `TRUSTED_GATEWAY_IP` | — | IP of the gateway that sets X-Forwarded-For; used to extract real actor IP |

## registry-auth

`services/auth/.env.example`

### General

| Variable | Example / default | Description |
|---|---|---|
| `LOG_LEVEL` | `info` | ── Server ──────────────────────────────────────────────────────────────────── |
| `LOG_FORMAT` | `json` | — |
| `GRPC_ADDR` | `:50051` | — |
| `HTTP_ADDR` | `:8080` | — |
| `MTLS_REQUIRED` | `true` | ── mTLS — production fail-safe (REDESIGN-001 Q-004 / Phase 1.2). Set to "false" only in local dev. ── Leave MTLS_REQUIRED=false in dev to run without TLS (a warning is logged). Generate certs with: make dev-certs |
| `MTLS_CA_CERT_PATH` | — | — |
| `MTLS_CERT_PATH` | — | — |
| `MTLS_KEY_PATH` | — | — |
| `DB_DSN` | `postgres://registry:registry@postgres:5432/registry_auth?sslmode=require` | ── Database ────────────────────────────────────────────────────────────────── sslmode=require is mandatory; sslmode=disable is rejected at startup. |
| `DB_MAX_CONNS` | `20` | — |
| `DB_MIN_CONNS` | `2` | — |
| `DB_CONNECT_TIMEOUT` | `5s` | — |
| `DB_MAX_CONN_LIFETIME` | `30m` | — |
| `DB_MAX_CONN_IDLE_TIME` | `5m` | — |
| `REDIS_ADDR` | `redis:6379` | ── Redis (JWT JTI revocation + per-IP rate limiting) ───────────────────────── |
| `REDIS_PASSWORD` | — | — |
| `REDIS_DB` | `0` | — |
| `JWT_PRIVATE_KEY_B64` | `<base64-encoded-pem-private-key>` | ── JWT RS256 signing keys ───────────────────────────────────────────────────── Both are base64-encoded PEM blocks. Private key: PKCS8. Public key: PKIX. See generation instructions at the top of this file.  Phase 6.5 — JWT key material can come from EITHER the single-key trio below OR the multi-key ring (JWT_KEY_RING_PATH). Pick exactly one path; setting both is rejected at startup so the operator cannot end up with two competing key sources. |
| `JWT_PUBLIC_KEY_B64` | `<base64-encoded-pem-public-key>` | — |
| `JWT_KEY_ID` | `<uuid>` | Unique identifier for this key pair; used as the JWKS kid header. |
| `JWT_KEY_RING_PATH` | — | ── JWT multi-key ring (Phase 6.5 — online rotation prep) ──────────────────── Optional path to a directory of PEM-encoded RSA private keys. Each file becomes one entry in the in-memory ring; the kid is the file's base name (e.g. "2026-06-30.pem" → kid "2026-06-30").  When set, JWT_PRIVATE_KEY_B64 / JWT_PUBLIC_KEY_B64 / JWT_KEY_ID MUST be empty. Validation accepts any kid in the ring; signing uses the kid named in JWT_SIGNING_KID below (or the lexicographically-greatest kid if JWT_SIGNING_KID is empty). The JWKS endpoint enumerates every public key.  Rotation workflow: 1. Drop a new PEM file (e.g. "2026-07-15.pem") next to the current one. 2. Restart auth — the new key is added to the ring + auto-promoted to signer (last-wins lexicographic ordering). 3. Wait at least JWT TTL (300 s) for live tokens minted by the old kid to drain. 4. Delete the old PEM file. 5. Restart auth — the old kid is gone from the ring and from JWKS.  Per CLAUDE.md §7: if JWT_KEY_RING_PATH is set but unreadable, contains no PEM files, or contains a malformed PEM, the service fails to start. |
| `JWT_SIGNING_KID` | — | Optional explicit signing kid override. Empty = use lex-greatest kid in the ring. |
| `SSO_CREDENTIAL_KEY_HEX` | — | ── SSO (FE-API-034) ────────────────────────────────────────────────────────── 32-byte AES-256 key used to encrypt OAuth client_secret values at rest. Generate with: openssl rand -hex 32 Leave empty to disable SSO entirely (routes are not registered). |
| `MFA_SECRET_KEY_HEX` | — | ── MFA (TOTP) ──────────────────────────────────────────────────────────────── 32-byte (64 hex char) AES-256 key-encryption key for encrypting users' TOTP secrets (users.mfa_secret_enc) at rest. SEPARATE from SSO_CREDENTIAL_KEY_HEX. Generate with: openssl rand -hex 32 Required — the service fails to start if empty or not exactly 32 bytes. No default. Rotate via the rekey tool with its own key material: registry-auth rotate-kek --mfa   (KEK_OLD_HEX/KEK_NEW_HEX = old/new MFA KEK) See infra/runbooks/kek-rotation.md. |
| `MFA_SECRET_KEK_VERSION` | — | KEK generation stamped on freshly-enrolled MFA secrets (users.mfa_secret_kek_version). Defaults to 1 when unset. When you rotate the MFA KEK (the rekey --mfa sweep bumps every existing row to the new generation), set this to the SAME new value so subsequent enrolments stamp the current generation rather than a stale 1. Positive integer; unset/0 = 1. |
| `SSO_BASE_URL` | `http://localhost:8080` | Public origin used to build OAuth redirect_uri. MUST match what the IdP has registered for this application (Google/GitHub/Microsoft + generic OIDC). |
| `SAML_SP_CERT_PATH` | — | ── SAML Service Provider keypair ──────────────────────────────────────────── SAML requires the SP (this service) to present an X.509 cert + RSA key when signing AuthnRequests. The IdP uses the embedded public key to verify our request signatures.  Leave both empty to disable SAML support entirely (the routes return 501). In production set both paths and chmod 600 on the key (CLAUDE.md §7).  Generate a self-signed dev keypair: openssl req -x509 -newkey rsa:2048 -nodes -keyout saml-sp.key -out saml-sp.crt \ -days 365 -subj "/CN=registry-auth-saml-sp" chmod 600 saml-sp.key  Production: rotate via cert-manager + an internal CA issuer, same model as the mTLS certs. |
| `SAML_SP_KEY_PATH` | — | — |
| `OTEL_EXPORTER` | `jaeger` | ── OpenTelemetry ───────────────────────────────────────────────────────────── Exporters: jaeger \| tempo \| datadog \| stdout |
| `OTEL_ENDPOINT` | `http://jaeger:4317` | — |
| `OTEL_SERVICE_NAME` | `registry-auth` | — |
| `OTEL_ENVIRONMENT` | `development` | — |
| `OTEL_SAMPLING_RATE` | `1.0` | — |

## registry-core

`services/core/.env.example`

### Server

| Variable | Example / default | Description |
|---|---|---|
| `GRPC_ADDR` | `:50051` | — |
| `HTTP_ADDR` | `:8080` | — |
| `LOG_LEVEL` | `info` | — |
| `LOG_FORMAT` | `json` | — |

### mTLS

| Variable | Example / default | Description |
|---|---|---|
| `MTLS_REQUIRED` | `true` | — |
| `MTLS_CA_CERT_PATH` | `/certs/ca.crt` | — |
| `MTLS_CERT_PATH` | `/certs/core.crt` | — |
| `MTLS_KEY_PATH` | `/certs/core.key` | — |

### Upstream service addresses

| Variable | Example / default | Description |
|---|---|---|
| `AUTH_GRPC_ADDR` | `registry-auth:50051` | — |
| `AUTH_REALM` | `http://localhost:8080/auth/token` | Externally reachable URL returned in WWW-Authenticate realm (Docker clients must be able to reach this) |
| `METADATA_GRPC_ADDR` | `registry-metadata:50051` | — |
| `STORAGE_GRPC_ADDR` | `registry-storage:50051` | — |

### Redis

| Variable | Example / default | Description |
|---|---|---|
| `REDIS_ADDR` | `localhost:6379` | — |
| `REDIS_PASSWORD` | — | — |

### RabbitMQ

| Variable | Example / default | Description |
|---|---|---|
| `RABBITMQ_URL` | `amqp://guest:guest@localhost:5672/` | — |

### Pull-activity tracking

| Variable | Example / default | Description |
|---|---|---|
| `PULL_EVENT_SAMPLE_RATE` | `1.0` | Probability that a successful manifest GET publishes a pull.image event. Range: [0.0, 1.0]. Default 1.0 (publish every pull).  Reducing this loses FE-API-030 analytics precision (metric=pulls) proportionally. The FE-API-043 max_idle_days retention rule rides services/metadata's 24h-debounced last_pulled_at update, so its accuracy is preserved as long as this rate is > 0. Set to 0.0 to disable the publish entirely (analytics will return zeros and max_idle_days retention will stop tracking new pulls). |

### Observability

| Variable | Example / default | Description |
|---|---|---|
| `OTEL_EXPORTER` | `stdout` | — |
| `OTEL_ENDPOINT` | — | — |
| `OTEL_SERVICE_NAME` | `registry-core` | — |
| `OTEL_ENVIRONMENT` | `development` | — |

## registry-gateway

`services/gateway/.env.example`

### Server

| Variable | Example / default | Description |
|---|---|---|
| `GRPC_ADDR` | `:50051` | — |
| `HTTP_ADDR` | `:8080` | — |
| `LOG_LEVEL` | `info` | — |
| `LOG_FORMAT` | `json` | — |

### mTLS

| Variable | Example / default | Description |
|---|---|---|
| `MTLS_REQUIRED` | `true` | — |
| `MTLS_CA_CERT_PATH` | `/certs/ca.crt` | — |
| `MTLS_CERT_PATH` | `/certs/gateway.crt` | — |
| `MTLS_KEY_PATH` | `/certs/gateway.key` | — |

### Observability

| Variable | Example / default | Description |
|---|---|---|
| `OTEL_EXPORTER` | `stdout` | — |
| `OTEL_ENDPOINT` | — | — |
| `OTEL_SERVICE_NAME` | `registry-gateway` | — |
| `OTEL_ENVIRONMENT` | `development` | — |

## registry-gc

`services/gc/.env.example`

### Server

| Variable | Example / default | Description |
|---|---|---|
| `GRPC_ADDR` | `:50051` | — |
| `HTTP_ADDR` | `:8080` | — |
| `LOG_LEVEL` | `info` | — |
| `LOG_FORMAT` | `json` | — |

### mTLS

| Variable | Example / default | Description |
|---|---|---|
| `MTLS_REQUIRED` | `true` | — |
| `MTLS_CA_CERT_PATH` | `/certs/ca.crt` | — |
| `MTLS_CERT_PATH` | `/certs/gc.crt` | — |
| `MTLS_KEY_PATH` | `/certs/gc.key` | — |

### Observability

| Variable | Example / default | Description |
|---|---|---|
| `OTEL_EXPORTER` | `stdout` | — |
| `OTEL_ENDPOINT` | — | — |
| `OTEL_SERVICE_NAME` | `registry-gc` | — |
| `OTEL_ENVIRONMENT` | `development` | — |

### Upstream services

| Variable | Example / default | Description |
|---|---|---|
| `METADATA_GRPC_ADDR` | `registry-metadata:50051` | — |
| `STORAGE_GRPC_ADDR` | `registry-storage:50051` | — |
| `RABBITMQ_URL` | `amqp://guest:password@rabbitmq:5672/` | — |

### GC schedule and policy

| Variable | Example / default | Description |
|---|---|---|
| `GC_MODE` | `full` | — |
| `GC_RUN_INTERVAL_HOURS` | `24` | — |
| `GC_BLOB_MIN_AGE_HOURS` | `1` | — |
| `GC_MANIFEST_MIN_AGE_HOURS` | `24` | — |

### FE-API-032 GC run persistence

| Variable | Example / default | Description |
|---|---|---|
| `DB_DSN` | `postgres://registry_gc:password@postgres:5432/registry_gc?sslmode=disable` | DB_DSN enables the gc_runs table + GCService gRPC surface (GetStatus, RunNow, ListRuns). Leave unset for backwards-compatible cron-only mode. |
| `DB_MAX_CONNS` | `10` | — |

## registry-management

`services/management/.env.example`

### General

| Variable | Example / default | Description |
|---|---|---|
| `HTTP_ADDR` | `:8085` | HTTP listen address (default :8085) |
| `AUTH_GRPC_ADDR` | `localhost:50051` | gRPC addresses of upstream services |
| `METADATA_GRPC_ADDR` | `localhost:50053` | — |
| `SCANNER_GRPC_ADDR` | — | Optional gRPC clients — leave empty to disable the corresponding routes. SCANNER_GRPC_ADDR enables FE-API-018 (scan policies) and FE-API-019 (compliance reports). Empty: routes return 404 "route disabled". |
| `SIGNER_GRPC_ADDR` | — | SIGNER_GRPC_ADDR enables FE-API-003 signature lookups. |
| `WEBHOOK_GRPC_ADDR` | — | WEBHOOK_GRPC_ADDR enables FE-API-021..024 webhook routes. |
| `TENANT_GRPC_ADDR` | — | TENANT_GRPC_ADDR enables /api/v1/admin/tenants + /api/v1/workspace/me. |
| `PROXY_GRPC_ADDR` | — | PROXY_GRPC_ADDR enables FUT-013 /api/v1/proxy/cache routes (list + stats + evict). Empty leaves those routes returning 404 "route disabled". The frontend probes and hides the sidebar entry when 404 lands. |
| `CORE_GRPC_ADDR` | — | CORE_GRPC_ADDR enables the OCI referrers route (GET /api/v1/repositories/{org}/{repo}/tags/{tag}/referrers). Empty leaves that route returning 404 "route disabled". The frontend probes and hides the Referrers tab when 404 lands. |
| `PUBLIC_BASE_URL` | — | PUBLIC_BASE_URL is the fully-qualified, scheme-included URL at which this management BFF is reachable from the public internet (e.g. https://registry.example.com). Optional — used only by the FUT-023 PR-registry config route to render the GitHub-webhook receiver URL (<PUBLIC_BASE_URL>/webhooks/scm/github/pr) that an admin pastes into GitHub. Empty: the config route returns an empty webhook_url rather than guessing. |
| `CORS_ALLOWED_ORIGIN` | `http://localhost:5173` | CORS — must match the frontend origin exactly; never use * Dev: http://localhost:5173  \|  Prod: https://registry.yourdomain.io |
| `MTLS_REQUIRED` | `true` | mTLS — production fail-safe (REDESIGN-001 Q-004 / Phase 1.2). Set to "false" only in local dev. |
| `MTLS_CA_CERT_PATH` | — | — |
| `MTLS_CERT_PATH` | — | — |
| `MTLS_KEY_PATH` | — | — |
| `LOG_FORMAT` | `text` | Logging |
| `LOG_LEVEL` | `info` | — |
| `OTEL_EXPORTER` | `stdout` | OpenTelemetry |
| `OTEL_ENDPOINT` | — | — |
| `OTEL_SERVICE_NAME` | `registry-management` | — |
| `OTEL_ENVIRONMENT` | `development` | — |
| `OTEL_SAMPLING_RATE` | `1.0` | — |

## registry-mcp

`services/mcp/.env.example`

### Logging

| Variable | Example / default | Description |
|---|---|---|
| `LOG_LEVEL` | `info` | LOG_FORMAT=json ensures Claude Desktop's log capture stays parseable. LOG_LEVEL=info is quiet enough for a persistent stdio-transport binary where every stray log to stdout would corrupt the JSON-RPC stream. |
| `LOG_FORMAT` | `json` | — |

### Transport

| Variable | Example / default | Description |
|---|---|---|
| `MCP_TRANSPORT` | `stdio` | stdio  — Claude Desktop / Cursor stdio launch this binary as a child process and exchange MCP JSON-RPC frames over stdin/stdout. http   — remote clients (Cursor remote, continue.dev) POST JSON-RPC frames to MCP_HTTP_ADDR. Compose defaults to this. |
| `MCP_HTTP_ADDR` | `:8092` | — |

### Backend

| Variable | Example / default | Description |
|---|---|---|
| `MCP_MANAGEMENT_URL` | `http://registry-management:8091` | MCP_MANAGEMENT_URL is the BFF root. Every tool proxies through this URL — no direct gRPC dial-outs from the MCP surface. Keeps the tool permissions honest to what an operator could do in the dashboard. |
| `MCP_API_KEY` | — | MCP_API_KEY is a service-account key issued from /api-keys with read scopes. Format: key.<uuid>.<64-hex-secret> (FUT-006). Revoke any time from the dashboard — the MCP server treats it as opaque. |
| `MCP_TENANT_ID` | — | MCP_TENANT_ID pins the tenant whose data the MCP surface exposes. The platform is single-tenant, so this is the bootstrap tenant id emitted by the registry-auth bootstrap CLI. UUID. |

## registry-metadata

`services/metadata/.env.example`

### Server

| Variable | Example / default | Description |
|---|---|---|
| `GRPC_ADDR` | `:50051` | — |
| `HTTP_ADDR` | `:8080` | — |
| `LOG_LEVEL` | `info` | — |
| `LOG_FORMAT` | `json` | — |

### mTLS

| Variable | Example / default | Description |
|---|---|---|
| `MTLS_REQUIRED` | `true` | — |
| `MTLS_CA_CERT_PATH` | `/certs/ca.crt` | — |
| `MTLS_CERT_PATH` | `/certs/metadata.crt` | — |
| `MTLS_KEY_PATH` | `/certs/metadata.key` | — |

### Database

| Variable | Example / default | Description |
|---|---|---|
| `DB_DSN` | `postgres://metadata:password@localhost:5432/registry_metadata?sslmode=require` | — |
| `DB_DSN_REPLICA` | `   # optional; read-heavy queries route here when set` | — |
| `DB_MAX_CONNS` | `20` | — |
| `DB_MIN_CONNS` | `2` | — |

### Redis

| Variable | Example / default | Description |
|---|---|---|
| `REDIS_ADDR` | `localhost:6379` | — |
| `REDIS_PASSWORD` | — | — |
| `METADATA_CACHE_ENABLED` | `true` | — |

### RabbitMQ

| Variable | Example / default | Description |
|---|---|---|
| `RABBITMQ_URL` | `amqp://guest:guest@localhost:5672/` | Drives manifests.last_pulled_at updates so the FE-API-043 max_idle_days retention rule has a column to evaluate. When empty the consumer is disabled (service still starts; emits a startup WARN). |

### Ephemeral PR-scoped registries

| Variable | Example / default | Description |
|---|---|---|
| `PR_REGISTRY_KEY_HEX` | — | 32-byte AES-256-GCM KEK (64 hex chars) sealing the per-tenant GitHub webhook secret used by ephemeral PR-scoped registries. UNSET disables the PR-registry feature entirely. SET-but-not-64-hex-chars fails closed at startup (a bad KEK would silently corrupt the sealed secret). |

### Observability

| Variable | Example / default | Description |
|---|---|---|
| `OTEL_EXPORTER` | `stdout` | — |
| `OTEL_ENDPOINT` | — | — |
| `OTEL_SERVICE_NAME` | `registry-metadata` | — |
| `OTEL_ENVIRONMENT` | `development` | — |

## registry-proxy

`services/proxy/.env.example`

### Server

| Variable | Example / default | Description |
|---|---|---|
| `GRPC_ADDR` | `:50055` | — |
| `HTTP_ADDR` | `:8084` | — |
| `LOG_LEVEL` | `info` | — |
| `LOG_FORMAT` | `json` | — |

### Database

| Variable | Example / default | Description |
|---|---|---|
| `DB_DSN` | `postgres://proxy:password@localhost:5432/proxy?sslmode=require` | — |
| `DB_MAX_CONNS` | `20` | — |

### Redis

| Variable | Example / default | Description |
|---|---|---|
| `REDIS_ADDR` | `localhost:6379` | — |
| `REDIS_PASSWORD` | — | — |
| `REDIS_DB` | `0` | — |

### Upstream service addresses

| Variable | Example / default | Description |
|---|---|---|
| `AUTH_GRPC_ADDR` | `registry-auth:50051` | — |
| `STORAGE_GRPC_ADDR` | `registry-storage:50052` | — |

### Credential encryption

| Variable | Example / default | Description |
|---|---|---|
| `CREDENTIAL_KEY_HEX` | — | 64-character hex string (32 bytes) — generate with: openssl rand -hex 32 |

### Upstream HTTP client

| Variable | Example / default | Description |
|---|---|---|
| `UPSTREAM_HTTP_TIMEOUT_SECS` | `30` | — |
| `UPSTREAM_MAX_RESPONSE_BYTES` | `21474836480` | — |

### mTLS

| Variable | Example / default | Description |
|---|---|---|
| `MTLS_REQUIRED` | `true` | — |
| `MTLS_CA_CERT_PATH` | `/certs/ca.crt` | — |
| `MTLS_CERT_PATH` | `/certs/proxy.crt` | — |
| `MTLS_KEY_PATH` | `/certs/proxy.key` | — |

### Observability

| Variable | Example / default | Description |
|---|---|---|
| `OTEL_EXPORTER` | `stdout` | — |
| `OTEL_ENDPOINT` | — | — |
| `OTEL_SERVICE_NAME` | `registry-proxy` | — |
| `OTEL_ENVIRONMENT` | `development` | — |
| `OTEL_SAMPLING_RATE` | `1.0` | — |

## registry-scanner

`services/scanner/.env.example`

### Server

| Variable | Example / default | Description |
|---|---|---|
| `GRPC_ADDR` | `:50051` | — |
| `HTTP_ADDR` | `:8080` | — |
| `LOG_LEVEL` | `info` | — |
| `LOG_FORMAT` | `json` | — |

### mTLS

| Variable | Example / default | Description |
|---|---|---|
| `MTLS_REQUIRED` | `true` | — |
| `MTLS_CA_CERT_PATH` | `/certs/ca.crt` | — |
| `MTLS_CERT_PATH` | `/certs/scanner.crt` | — |
| `MTLS_KEY_PATH` | `/certs/scanner.key` | — |

### Observability

| Variable | Example / default | Description |
|---|---|---|
| `OTEL_EXPORTER` | `stdout` | — |
| `OTEL_ENDPOINT` | — | — |
| `OTEL_SERVICE_NAME` | `registry-scanner` | — |
| `OTEL_ENVIRONMENT` | `development` | — |

### RabbitMQ

| Variable | Example / default | Description |
|---|---|---|
| `RABBITMQ_URL` | `amqp://guest:guest@rabbitmq:5672/` | — |

### Database

| Variable | Example / default | Description |
|---|---|---|
| `DB_DSN` | `postgres://scanner:scanner@postgres:5432/scanner?sslmode=disable` | — |
| `DB_MAX_CONNS` | `20` | — |

### Compliance report worker

| Variable | Example / default | Description |
|---|---|---|
| `REPORT_OUTPUT_DIR` | `/tmp/reports` | Output directory for rendered PDF + SPDX JSON artifacts. Production should replace this with object storage + signed URLs; the v1 implementation uses local files. Reports are written to <REPORT_OUTPUT_DIR>/<tenant>/<id>.{pdf,spdx.json}. |
| `REPORT_POLL_INTERVAL_SECS` | `5` | How often the worker polls compliance_reports for pending rows. |

### Upstream gRPC services

| Variable | Example / default | Description |
|---|---|---|
| `METADATA_GRPC_ADDR` | `registry-metadata:50051` | — |
| `STORAGE_GRPC_ADDR` | `registry-storage:50051` | — |

### Scanner plugin

| Variable | Example / default | Description |
|---|---|---|
| `SCANNER_PLUGIN_PATH` | — | Path to the scanner binary (e.g. /plugins/trivy-wrapper) |
| `SCANNER_PLUGIN_CHECKSUM` | — | SHA256 hex checksum of the plugin binary — service refuses to start on mismatch |
| `SCANNER_WORKER_COUNT` | `4` | Number of concurrent scan workers (default: 4) |
| `SCANNER_JOB_TIMEOUT_SECS` | `600` | Per-job timeout in seconds (default: 600) |

## registry-signer

`services/signer/.env.example`

### Server

| Variable | Example / default | Description |
|---|---|---|
| `GRPC_ADDR` | `:50051` | — |
| `HTTP_ADDR` | `:8080` | — |
| `LOG_LEVEL` | `info` | — |
| `LOG_FORMAT` | `json` | — |

### mTLS

| Variable | Example / default | Description |
|---|---|---|
| `MTLS_REQUIRED` | `true` | — |
| `MTLS_CA_CERT_PATH` | `/certs/ca.crt` | — |
| `MTLS_CERT_PATH` | `/certs/signer.crt` | — |
| `MTLS_KEY_PATH` | `/certs/signer.key` | — |

### Observability

| Variable | Example / default | Description |
|---|---|---|
| `OTEL_EXPORTER` | `stdout` | — |
| `OTEL_ENDPOINT` | — | — |
| `OTEL_SERVICE_NAME` | `registry-signer` | — |
| `OTEL_ENVIRONMENT` | `development` | — |

### Signing key

| Variable | Example / default | Description |
|---|---|---|
| `SIGNER_KEY_BACKEND` | `env` | Generate with: openssl ecparam -name prime256v1 -genkey -noout \| openssl pkcs8 -topk8 -nocrypt \| base64 -w0 |
| `SIGNER_COSIGN_PRIVATE_KEY` | `<base64-encoded PEM PKCS8 ECDSA P-256 private key>` | — |
| `SIGNER_COSIGN_PUBLIC_KEY` | `<base64-encoded PEM PKIX ECDSA P-256 public key>` | — |

### Database

| Variable | Example / default | Description |
|---|---|---|
| `SIGNER_DB_DSN` | `postgres://signer:changeme@localhost:5432/registry_signer?sslmode=require` | Required in production for durable signature persistence. Without this the signer falls back to an in-memory store and all signature records are lost on restart (replicas also have independent stores). sslmode=require is mandatory; sslmode=disable is rejected at startup. |

## registry-storage

`services/storage/.env.example`

### Server

| Variable | Example / default | Description |
|---|---|---|
| `GRPC_ADDR` | `:50051` | — |
| `HTTP_ADDR` | `:8080` | — |
| `LOG_LEVEL` | `info` | — |
| `LOG_FORMAT` | `json` | — |

### mTLS

| Variable | Example / default | Description |
|---|---|---|
| `MTLS_REQUIRED` | `true` | — |
| `MTLS_CA_CERT_PATH` | `/certs/ca.crt` | — |
| `MTLS_CERT_PATH` | `/certs/storage.crt` | — |
| `MTLS_KEY_PATH` | `/certs/storage.key` | — |

### Storage driver

| Variable | Example / default | Description |
|---|---|---|
| `STORAGE_DRIVER` | `minio` | — |
| `STORAGE_MINIO_ENDPOINT` | `minio:9000` | MinIO |
| `STORAGE_MINIO_ACCESS_KEY` | — | — |
| `STORAGE_MINIO_SECRET_KEY` | — | — |
| `STORAGE_MINIO_BUCKET` | `registry` | — |
| `STORAGE_MINIO_USE_SSL` | `true` | — |
| `STORAGE_S3_BUCKET` | — | AWS S3 |
| `STORAGE_S3_REGION` | — | — |
| `STORAGE_GCS_BUCKET` | — | GCS |
| `STORAGE_GCS_PROJECT` | — | — |
| `STORAGE_AZURE_CONTAINER` | — | Azure |
| `STORAGE_AZURE_ACCOUNT` | — | — |
| `STORAGE_FILESYSTEM_ROOT` | `/data` | Filesystem (dev only) |

### Observability

| Variable | Example / default | Description |
|---|---|---|
| `OTEL_EXPORTER` | `stdout` | — |
| `OTEL_ENDPOINT` | — | — |
| `OTEL_SERVICE_NAME` | `registry-storage` | — |
| `OTEL_ENVIRONMENT` | `development` | — |

## registry-tenant

`services/tenant/.env.example`

### Server

| Variable | Example / default | Description |
|---|---|---|
| `GRPC_ADDR` | `:50051` | — |
| `HTTP_ADDR` | `:8080` | — |
| `LOG_LEVEL` | `info` | — |
| `LOG_FORMAT` | `json` | — |

### mTLS

| Variable | Example / default | Description |
|---|---|---|
| `MTLS_REQUIRED` | `true` | — |
| `MTLS_CA_CERT_PATH` | `/certs/ca.crt` | — |
| `MTLS_CERT_PATH` | `/certs/tenant.crt` | — |
| `MTLS_KEY_PATH` | `/certs/tenant.key` | — |

### Observability

| Variable | Example / default | Description |
|---|---|---|
| `OTEL_EXPORTER` | `stdout` | — |
| `OTEL_ENDPOINT` | — | — |
| `OTEL_SERVICE_NAME` | `registry-tenant` | — |
| `OTEL_ENVIRONMENT` | `development` | — |

### Database

| Variable | Example / default | Description |
|---|---|---|
| `DB_DSN` | `postgres://tenant:password@localhost:5432/registry_tenant?sslmode=require` | — |
| `DB_MAX_CONNS` | `20` | — |

### Redis

| Variable | Example / default | Description |
|---|---|---|
| `REDIS_ADDR` | `redis:6379` | — |
| `REDIS_PASSWORD` | — | — |
| `REDIS_DB` | `0` | — |

### Platform domain

| Variable | Example / default | Description |
|---|---|---|
| `PLATFORM_BASE_DOMAIN` | `registry.localhost` | Wildcard zone used to build fallback tenant hostnames `<slug>.<base>`. Each tenant gets a unique registry hostname under this zone unless it has a verified primary custom domain. Defaults to registry.localhost for dev. |

## registry-webhook

`services/webhook/.env.example`

### Server

| Variable | Example / default | Description |
|---|---|---|
| `GRPC_ADDR` | `:50051` | — |
| `HTTP_ADDR` | `:8080` | — |
| `LOG_LEVEL` | `info` | — |
| `LOG_FORMAT` | `json` | — |

### mTLS

| Variable | Example / default | Description |
|---|---|---|
| `MTLS_REQUIRED` | `true` | — |
| `MTLS_CA_CERT_PATH` | `/certs/ca.crt` | — |
| `MTLS_CERT_PATH` | `/certs/webhook.crt` | — |
| `MTLS_KEY_PATH` | `/certs/webhook.key` | — |

### Observability

| Variable | Example / default | Description |
|---|---|---|
| `OTEL_EXPORTER` | `stdout` | — |
| `OTEL_ENDPOINT` | — | — |
| `OTEL_SERVICE_NAME` | `registry-webhook` | — |
| `OTEL_ENVIRONMENT` | `development` | — |

### Database

| Variable | Example / default | Description |
|---|---|---|
| `DB_DSN` | `postgres://webhook:secret@postgres:5432/registry_webhook?sslmode=require` | — |
| `DB_MAX_CONNS` | `20` | — |

### RabbitMQ

| Variable | Example / default | Description |
|---|---|---|
| `RABBITMQ_URL` | `amqp://guest:guest@rabbitmq:5672/` | — |

### Credentials

| Variable | Example / default | Description |
|---|---|---|
| `CREDENTIAL_KEY_HEX` | — | 32-byte AES key for encrypting HMAC secrets at rest. Generate: openssl rand -hex 32 |

### Delivery

| Variable | Example / default | Description |
|---|---|---|
| `DELIVERY_POLL_INTERVAL_SECS` | `5` | — |
| `DELIVERY_TIMEOUT_SECS` | `30` | — |


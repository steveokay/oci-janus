# Integrations catalog

OCI-Janus is built to plug into the tools you already run. This page is the
one-stop catalogue of every external and pluggable surface: what it does, how you
turn it on, and where the deep reference lives.

| Integration | What it connects to | How you configure it |
|---|---|---|
| [Storage backends](storage.md) | MinIO, S3-compatible object stores, filesystem | `STORAGE_DRIVER` + per-driver env |
| [Single sign-on](#single-sign-on-sso) | Google / GitHub / Microsoft / generic OIDC, SAML 2.0 | Deployment config + `SSO_CREDENTIAL_KEY_HEX` |
| [Vulnerability scanners](#vulnerability-scanners) | Trivy / Grype / Clair | External-process plugin + `SCANNER_PLUGIN_PATH` |
| [Image signing](#image-signing) | Cosign via Vault / cloud KMS | `SIGNER_KEY_BACKEND` |
| [Webhooks](#webhooks) | Your CI, ChatOps, supply-chain stack | Dashboard → Webhooks |
| [Notification channels](#notification-channels) | Email (Resend / SMTP), org webhook | Dashboard → Settings › Notifications |
| [SCM PR registries](#scm-pr-registries) | GitHub pull-request webhooks | Dashboard → Settings › Integrations + `PR_REGISTRY_KEY_HEX` |
| [AI agents (MCP)](#ai-agents-mcp) | Claude Desktop, Cursor, other MCP clients | The `registry-mcp` server |

!!! note "Secrets are key-encrypted at rest"
    Integrations that store a secret (SSO client secrets, webhook HMAC keys,
    email credentials, the PR-webhook secret) seal it with an AES-256-GCM
    **key-encryption key (KEK)** supplied as a 64-hex-character env var. The full
    KEK inventory and rotation procedure are in [Self-hosting](../SELF-HOSTING.md).

---

## Storage backends

Where blobs live. `STORAGE_DRIVER` selects the backend; **MinIO** (and any
S3-compatible store) and **filesystem** are implemented today, with S3/GCS/Azure
driver slots recognised for the roadmap.

Because the MinIO driver speaks the S3 API, you can point it at AWS S3 or any
S3-compatible store by setting the endpoint. Full per-driver env tables,
encryption-at-rest notes, and worked examples are on the dedicated page:

**→ [Storage backends](storage.md)**

## Single sign-on (SSO)

Deployment-wide SSO via **OAuth 2.0 + PKCE** (Google, GitHub, Microsoft, or a
generic OIDC provider) and **SAML 2.0**. There is one global configuration per
provider — not per-tenant.

- **Enable it** by setting `SSO_CREDENTIAL_KEY_HEX` (the KEK that seals OAuth
  client secrets at rest — SSO is disabled while it is unset) and the public
  origin used for redirect URIs.
- **SAML** additionally needs a service-provider keypair
  (`SAML_SP_CERT_PATH` / `SAML_SP_KEY_PATH`); `SSO_SAML_TRUST_EMAIL` controls
  whether an IdP-asserted email is trusted as verified.
- Providers appear as buttons on the login page once configured.

!!! note "Configured at the deployment level"
    SSO is wired through deployment configuration, not the dashboard — the
    Settings → Workspace tab shows a **read-only** SSO posture card. Rotating a
    secret or adding a provider means updating the config and redeploying.

**→ Deep dive: [Authentication](../AUTH.md) · [SAML SSO](../SAML.md)**

## Vulnerability scanners

Scanning runs through an **external-process plugin** rather than a built-in CVE
engine, so you can bring **Trivy**, **Grype**, or **Clair**.

- Point `SCANNER_PLUGIN_PATH` at the adapter binary and set
  `SCANNER_PLUGIN_CHECKSUM` to its SHA-256 — the scanner refuses to start on a
  checksum mismatch (a supply-chain guard on the adapter itself).
- Choose the blocking severity and auto-scan behaviour under [Security ›
  Policies](../guide/security.md#policies) (or Settings › Scanning).
- Adapter health and a test-scan button live on the same screens.

The dev stack ships a Trivy adapter under the `scanner` compose profile.

**→ Deep dive: [Vulnerability scanning](../SCANNER.md)**

## Image signing

**Cosign** (Sigstore) signing and verification (Notary v2 is deferred). Private
keys never leave the key store:

- `SIGNER_KEY_BACKEND` selects the backend — `env` for local dev, `vault` for
  self-hosted, or a cloud KMS (AWS/GCP/Azure) in production.
- The signer only asks the backend to sign/verify; it never handles raw private
  keys in production.
- Per-repo **trusted-key** allowlists and **require-signature** pull policies are
  set on each [repository's Settings tab](../guide/repositories.md#repository-settings).

**→ Deep dive: [Image signing](../SIGNING.md)**

## Webhooks

Stream registry events to your own endpoints. Configure webhooks in the
dashboard (**Sidebar → Integrations → Webhooks**), not via env — see the
[operations guide](../guide/operations.md#webhooks).

- Every payload is **HMAC-SHA256 signed**; verify the `X-Registry-Signature:
  sha256=<hex>` header with the secret issued at creation.
- **HTTPS-only** with an **SSRF block-list** (private ranges and the cloud
  metadata endpoint are rejected, re-checked at dial time against DNS rebinding).
- Failed deliveries retry with backoff (`5s → 30s → 5m → 30m → 2h`), then
  dead-letter. Inspect any delivery's full request/response from the webhook
  detail page.
- The per-endpoint secret is sealed at rest with the `CREDENTIAL_KEY_HEX` KEK.

**→ Deep dive: [Events](../EVENTS.md) for the routing keys + payload shapes.**

## Notification channels

Shared, org-scoped delivery for notification categories (scan results, access
reviews, and so on), configured by an admin under **Settings › Notifications**
(see the [settings guide](../guide/settings.md#notifications)):

- **Email** — **Resend** (HTTP API, the default) or **SMTP/Gmail**. Requires the
  `NOTIFY_EMAIL_KEY_HEX` KEK (sealing the API key / SMTP password). Recipient
  emails are resolved from `registry-auth` over gRPC (`AUTH_GRPC_ADDR`), and
  `PLATFORM_HOST` sets the base URL for links in the email body.
- **Webhook** — a single shared org webhook, HMAC-signed like the outbound
  webhooks above, gated by the `NOTIFY_WEBHOOK_KEY_HEX` KEK.

Each channel is disabled until its KEK is set. Secret fields are write-only.

**→ Deep dive: [Access reviews](../ACCESS-REVIEW.md)**

## SCM PR registries

Auto-provision an ephemeral `pr-<repo>-<N>` organization for each open GitHub
pull request, then tear it down when the PR closes (promoting tags to a durable
org on merge). Configured under **Settings › Integrations** (see the [settings
guide](../guide/settings.md#integrations)):

- Set the `PR_REGISTRY_KEY_HEX` KEK (seals the GitHub webhook secret) and
  `PUBLIC_BASE_URL` (so the dashboard can show you the receiver URL to paste into
  GitHub).
- GitHub posts to the unauthenticated receiver `POST /webhooks/scm/github/pr`;
  the **`X-Hub-Signature-256` HMAC** is the trust boundary, verified against your
  configured secret. Bad signatures get `401`; when the feature/KEK is unset the
  endpoint returns `404` so it can't be used as a probe oracle.
- An optional **promote-target org** receives merged PR tags.

**→ Deep dive: [Services](../SERVICES.md) (registry-metadata PR-registry section).**

## AI agents (MCP)

Connect an AI assistant — Claude Desktop, Cursor, continue.dev — to the registry
through the **`registry-mcp`** Model Context Protocol server. It exposes **12
read-only tools** (list repositories/tags, get manifests and scan reports, list
signatures, query audit events, and more), so an agent can answer questions like
*"which images contain log4j 2.14?"* without any write access.

- Runs over **stdio** (default, ideal for Claude Desktop) or **HTTP**
  (`MCP_TRANSPORT=http`, default `:8092`).
- Authenticates to the platform with a normal **service-account API key**
  (`MCP_API_KEY`) — the key is never exposed to the LLM.

**→ Deep dive: [Connect an AI agent (MCP)](../MCP.md)**

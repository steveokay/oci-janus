# OCI-Janus

**Self-hosted OCI Distribution Spec v1.1 registry, written in Go.** The feature
scope of Docker Hub / Harbor / ECR — image push/pull, vulnerability scanning,
signing, RBAC, tamper-evident audit — without the cloud bill or the operational
footprint of gluing a dozen services onto `distribution/distribution`.

[Get started in 4 commands :material-arrow-right:](getting-started.md){ .md-button .md-button--primary }
[View on GitHub :fontawesome-brands-github:](https://github.com/steveokay/oci-janus){ .md-button }

---

## What makes it different

Relative to a plain `distribution/distribution` deployment, OCI-Janus ships:

- **mTLS between every internal service** — not just at the edge. Certs hot-reload
  on renewal with a per-server peer-CN allowlist.
- **Multi-key JWT signing** (RS256 ring + JWKS rotation) plus API keys, global
  SSO (OAuth 2.0 + PKCE and SAML 2.0), and org/repo RBAC.
- **Pluggable storage** — MinIO, S3, GCS, Azure, or filesystem.
- **Pluggable vulnerability scanning** — Trivy, Grype, Clair via an
  external-process plugin host, with per-tenant scan policies and SPDX SBOMs.
- **Cosign image signing** (Notary v2 planned) and a pull-through cache for
  upstream registries.
- **Tamper-evident audit log** — Postgres `FORCE ROW LEVEL SECURITY`, a
  low-privilege runtime role, and a per-tenant SHA-256 hash chain so a
  compromised audit service cannot rewrite history.
- **Read-only MCP server** so AI assistants can safely inspect repositories,
  audit events, health, and promotions.
- **Optional multi-tenant mode** (`DEPLOYMENT_MODE=multi`) for operators who
  genuinely need SaaS-style isolation. The default is single-tenant.

## Where to go next

<div class="grid cards" markdown>

- :material-rocket-launch: **[Quick start](getting-started.md)**
  Clone, bring up the stack with Docker Compose, bootstrap the first admin, and
  `docker login` in under five minutes.

- :material-view-dashboard: **[Using the dashboard](guide/index.md)**
  A screen-by-screen walkthrough of the web console — repositories, security,
  access, settings, and operations.

- :material-server-network: **[Self-hosting](SELF-HOSTING.md)**
  Production deployment guidance — env vars, secrets, KEKs, TLS, and hardening.

- :material-shield-lock: **[Security & identity](AUTH.md)**
  Authentication, SSO, token policies, workload identity, and access reviews.

- :material-sitemap: **[Architecture](SERVICES.md)**
  The 14 services, their responsibilities, gRPC contracts, and event flows.

</div>

!!! note "This site is generated from the repository"
    Every reference page here is the same Markdown that lives in the
    [`docs/`](https://github.com/steveokay/oci-janus/tree/main/docs) tree of the
    repo. Canonical development rules live in
    [`CLAUDE.md`](https://github.com/steveokay/oci-janus/blob/main/CLAUDE.md);
    per-decision history lives in the [ADRs](adr/README.md).

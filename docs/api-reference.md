# API & automation

Everything the dashboard does is a REST call you can make yourself — from CI, a
script, Terraform, or `curl`. This page explains the API model and how to
authenticate; the exhaustive per-route contract lives in
[Services](SERVICES.md).

## Two HTTP surfaces

| Surface | Base | Serves |
|---|---|---|
| **Management BFF** (`registry-management`) | `/api/v1/…` | Repositories, tags, scans, signing, webhooks, notifications, GC, tenants, PR registries — the bulk of the dashboard's API. |
| **Auth service** (`registry-auth`) | direct HTTP | Login, SSO, MFA, sessions, API keys, service accounts, and workload-identity tokens. |

Both sit behind `registry-gateway` in a real deployment. The OCI Distribution
API itself (`/v2/…`, push/pull/list/referrers) is served by `registry-core` and
follows the [OCI Distribution Spec v1.1](https://github.com/opencontainers/distribution-spec)
— a standard `docker`/`oras`/`helm` client speaks it directly.

## Authentication

Authenticated requests carry a **Bearer token** in the `Authorization` header, in
one of two forms:

- **JWT (RS256)** — `Authorization: Bearer <jwt>`. This is what browsers and the
  dashboard use. Obtain one by logging in; refresh it before it expires.
- **API key** — `Authorization: Bearer key.<uuid>.<64-hex-secret>`. This is the
  automation path (CI, Terraform, scripts). Issue keys — personal or scoped to a
  **service account** — from the dashboard (see [Access & identity](guide/access.md)).

!!! note "API keys have empty roles by design"
    A synthesised API-key principal carries no roles, so admin-only handlers
    cleanly return **403** rather than 401. Grant the key's owner (or service
    account) the RBAC roles it needs, and use scopes to bound what it can do.

The exact mechanics — the JWT key ring/JWKS, the API-key Argon2 cache, and the
Bearer dispatch rules — are in [Authentication](AUTH.md).

### A worked example

```bash
# 1. Issue an API key in the dashboard (Access → API keys → Issue key),
#    then export it:
export JANUS_KEY="key.xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx.<64-hex-secret>"

# 2. Call the BFF. (Adjust the host to your gateway; localhost:8085 is the
#    dev management port.)
curl -s http://localhost:8085/api/v1/repositories \
  -H "Authorization: Bearer $JANUS_KEY"
```

## Conventions

- **Pagination** is always cursor-based: pass `page_size` and the `page_token`
  returned by the previous call — never numeric offsets.
- **Errors** map gRPC status codes to HTTP: `FAILED_PRECONDITION` → 409,
  `PERMISSION_DENIED` → 403, `NOT_FOUND` → 404, `RESOURCE_EXHAUSTED` → 429/503.
- **Tenancy** is implicit from your token in single mode (the bootstrap tenant).

## Postman collection

A Postman collection + environment seed the API surface for interactive
exploration:

- `docs/postman/registry-management.postman_collection.json`
- `docs/postman/registry-management.postman_environment.json`

Import both, set the environment's base URL and a Bearer token, and the
management endpoints are ready to call. See
[`docs/postman/README.md`](https://github.com/steveokay/oci-janus/tree/main/docs/postman)
for setup.

## CLI & credential helpers

For `docker`/`helm` login automation and CI credential wiring, see
[Credential helpers](CREDENTIAL-HELPERS.md) — it has copy-paste snippets for the
common runners.

## OpenAPI specification

A machine-readable **OpenAPI 3.0** document for the BFF is **generated from the
service route table** — see the interactive **[API explorer](api-spec.md)**, or
grab the raw [`openapi.json`](openapi.json) for Postman / SDK generation.

Generating it from code (`services/management/cmd/openapi-gen` parses the
`mux.Handle` registrations) rather than hand-authoring it means the paths,
methods, path parameters, and auth requirement can never drift — a CI drift-guard
regenerates the spec on every management change and fails if the committed copy
is stale. Regenerate locally with:

```bash
cd services/management && make openapi   # writes ../../docs/openapi.json
```

Request/query and response **body** schemas are being enriched incrementally; the
[Services](SERVICES.md) reference remains the fullest per-route contract for now.

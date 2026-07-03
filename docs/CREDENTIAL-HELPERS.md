[//]: # (FUT-002 — credential-helpers deep dive. Pair this with docs/AUTH.md for the Bearer dispatch mechanics and docs/WORKLOAD-IDENTITY.md for the machine-identity model.)

# Credential Helpers — Wiring a Service-Account Key into Your Tooling

> Canonical reference for the FUT-002 **credential helpers** surface. Read this
> when you want to authenticate a CI pipeline, a Kubernetes cluster, a Terraform
> run, or a GitHub Actions workflow against the registry using a service-account
> API key — and you'd rather copy a working snippet than assemble one by hand.
>
> For the underlying token model (how a `Bearer key.…` is dispatched and
> validated) see [`AUTH.md`](AUTH.md). For the service-account identity model
> (shadow users, scope allowlists, lifecycle) see
> [`WORKLOAD-IDENTITY.md`](WORKLOAD-IDENTITY.md).

---

## TL;DR

- **The dashboard generates the snippet; you supply the secret.** The
  **Credential helpers** page (`/api-keys/helpers`) renders copy-paste-ready
  `docker login`, Kubernetes `imagePullSecret`, Terraform, and GitHub Actions
  snippets for a service account you pick from a dropdown.
- **The secret is never baked into the snippet.** Every format references an
  env var (`$REGISTRY_API_KEY`, or `secrets.REGISTRY_API_KEY` for GHA) that you
  populate out of band. The dashboard never echoes key material into a
  shareable snippet.
- **Two inputs only:** the registry hostname (fetched from
  `GET /api/v1/registry-info`) and the service-account **name** (used purely as
  the `--username`, so the auth call is recognisable in CI logs).
- **The key itself is a service-account API key** minted on `registry-auth`.
  On the wire it takes the form `Bearer key.<uuid>.<64-hex-secret>`.
- **SEC-055:** the frontend sanitises the SA name through an *allowlist*
  (`[a-z0-9._-]`) before it lands in a shell or YAML context — defence-in-depth
  on top of the server-side SA-name regex.

---

## 1. What it does

The snippet renderer is a pure, side-effect-free function
(`buildSnippets` in `frontend/src/lib/credential-snippets.ts`). Given a
`{ hostname, saName }` pair it returns all four formats as ready-to-paste
strings. The renderer adds nothing the caller has to fill in except the secret,
which is intentionally referenced by env var and never interpolated.

The four supported formats (`SNIPPET_FORMATS`) are, in order:

1. `docker login`
2. `kubernetes Secret`
3. `terraform`
4. `GitHub Actions`

The live UI is `HelpersPanel` (`frontend/src/components/access/HelpersPanel.tsx`),
mounted at the `/api-keys/helpers` route. It:

- loads the registry hostname via `useRegistryInfo()` and the tenant's service
  accounts via `useServiceAccounts()`;
- defaults the picker to the first active service account;
- renders one tab per format with a **Copy** button that writes the active
  snippet to the clipboard;
- shows *"Create a service account first to see helpers."* when no SA exists.

Secrets are never rendered into the panel — the copy button copies exactly the
string `buildSnippets` produced, env-var placeholder and all.

---

## 2. The two inputs

### 2.1 Registry hostname — `GET /api/v1/registry-info`

The management BFF exposes an auth-gated route
(`services/management/internal/handler/registry_info.go`, registered at
`handler.go:304` behind the auth middleware) that returns the deployment's
externally-reachable registry hostname:

```json
{ "registry_host": "registry.example.com", "supports_oci_v1_1": true }
```

The value comes from the `PLATFORM_HOST` config var. If it's empty the route
returns `500` with `{"error":"PLATFORM_HOST not configured"}` rather than
emitting a blank hostname that would render as `docker login  ` (two spaces) —
the production config validator catches this at startup, and this guard covers
the dev-misconfig case. The frontend caches the response aggressively (the
hostname doesn't change during a session).

### 2.2 Service-account name

The `saName` is the human-readable service-account name (e.g. `ci-prod`). It is
used **only** for the `--username` slot so the login is recognisable in CI
logs — it is *not* the secret. The name is sanitised before rendering; see §5.

---

## 3. The four snippet formats

Each example below is the exact output shape for
`hostname = registry.example.com`, `saName = ci-prod`.

### 3.1 `docker login`

```bash
# Authenticate Docker to the registry using your API key.
# Replace $REGISTRY_API_KEY with the secret you copied at key creation.
echo "$REGISTRY_API_KEY" | docker login registry.example.com \
  --username ci-prod \
  --password-stdin
```

The secret is piped via `--password-stdin` (never passed as an argument that
would leak into shell history or the process table).

### 3.2 `kubernetes Secret`

```bash
# Kubernetes pull secret — generated via kubectl.
kubectl create secret docker-registry regcred \
  --docker-server=registry.example.com \
  --docker-username=ci-prod \
  --docker-password=$REGISTRY_API_KEY \
  --dry-run=client -o yaml
```

`--dry-run=client -o yaml` prints the Secret manifest to stdout so you can
review it (or pipe it into `kubectl apply -f -`) before it hits the cluster.

### 3.3 `terraform`

```hcl
# Terraform Docker provider — authenticates with the registry.
provider "docker" {
  registry_auth {
    address  = "registry.example.com"
    username = "ci-prod"
    password = var.registry_api_key
  }
}

variable "registry_api_key" {
  type      = string
  sensitive = true
}
```

The secret is threaded through a `sensitive = true` Terraform variable rather
than hardcoded into the provider block.

### 3.4 `GitHub Actions`

```yaml
# GitHub Actions — authenticate then push.
- name: Log in to registry
  uses: docker/login-action@v3
  with:
    registry: registry.example.com
    username: ci-prod
    password: ${{ secrets.REGISTRY_API_KEY }}
```

The secret is pulled from the repository's GitHub Actions secrets store
(`secrets.REGISTRY_API_KEY`).

---

## 4. Where the secret comes from — the service-account API key

The snippets reference `$REGISTRY_API_KEY` but don't create it. That secret is a
**service-account API key** minted on `registry-auth`.

### 4.1 Issuing the key

`POST /api/v1/service-accounts/{id}/api-keys` (auth-service HTTP handler,
`http_service_accounts.go`; admin/owner role required) creates a key owned by
the service account. Under the hood (`ServiceAccountService.IssueKey` in
`services/auth/internal/service/service_account.go`):

1. Confirms the SA exists and the requested scopes are a subset of the SA's
   `allowed_scopes` (otherwise `SCOPE_NOT_ALLOWED`).
2. Generates a cryptographically random secret — 32 bytes rendered as **64
   lowercase hex characters**.
3. Hashes it with **argon2id** before persistence. The raw secret is *never*
   stored; only its hash and a 12-char display prefix (`KeyPrefix`).
4. Returns the raw secret exactly once in the `key` field of the response —
   it cannot be recovered afterwards.

### 4.2 Form on the wire

The credential a client presents is the Bearer form
(`parseAPIKeyBearer` in `services/auth/internal/handler/http.go`, FUT-006):

```
Bearer key.<uuid>.<64-hex-secret>
```

- literal `key.` prefix — the discriminator that routes the token to
  `ValidateAPIKey` instead of JWT validation;
- `<uuid>` — the API key's id;
- `<64-hex-secret>` — the one-time secret from §4.1.

Anything without the `key.` prefix is treated as an RS256 JWT. On a successful
API-key validation, `registry-auth` synthesises claims whose subject is the SA's
shadow user and whose roles are intentionally **empty** — raw API keys carry
scopes, not RBAC roles. See [`AUTH.md`](AUTH.md) for the full dispatch contract.

That Bearer value (or, for `docker login`, the secret half of it) is what you
place into `$REGISTRY_API_KEY` / `secrets.REGISTRY_API_KEY` when you run a
generated snippet.

---

## 5. SEC-055 — SA-name sanitisation is defence-in-depth

The service-account name is interpolated into shell commands (`docker login`,
`kubectl`) and YAML/HCL. Before rendering, `buildSnippets` runs the name through
`sanitiseSAName`:

```ts
function sanitiseSAName(name: string): string {
  return name.replace(/[^a-z0-9._-]/g, "");
}
```

This is an **allowlist** — every character outside `[a-z0-9._-]` is stripped, so
a name like `evil;rm -rf` or `ci|exfil` can never reach a shell context intact.

The server-side SA-name regex (`^[a-z0-9]+([._-][a-z0-9]+)*$` in
`services/auth/internal/handler/http_service_accounts.go`) already rejects those
characters at create time, so in practice a malicious name never gets persisted.
The frontend sanitiser is **defence-in-depth on top of** that server-side gate:
because it's an allowlist rather than a blocklist, a future *loosening* of the
server regex can't silently widen the snippet's attack surface. The
`credential-snippets` test pins this contract — it asserts the `--username` slot
stays within `[a-z0-9._-]` for semicolons, pipes, newlines, spaces, unicode
control chars, and uppercase input.

---

## 6. End-to-end: from zero to an authenticated pull

```
1. Create a service account   (Access → Service accounts → New)
2. Issue an API key for it     → copy the one-time secret
3. Open Credential helpers     (/api-keys/helpers)
4. Pick the SA + a format tab  → Copy the snippet
5. Export the secret:            export REGISTRY_API_KEY=<paste>
6. Run the snippet             → docker/kubectl/terraform/GHA authenticates
```

Steps 1–2 mint the identity + secret; steps 3–4 produce the wiring; steps 5–6
run it. The secret only ever lives in your shell/secret store — never in the
dashboard's rendered output.

---

## 7. File reference

| File | Why it exists |
|---|---|
| `frontend/src/lib/credential-snippets.ts` | The pure `buildSnippets` renderer + `sanitiseSAName` allowlist (SEC-055) |
| `frontend/src/lib/__tests__/credential-snippets.test.ts` | Pins format coverage, hostname substitution, and the SEC-055 allowlist contract |
| `frontend/src/components/access/HelpersPanel.tsx` | Live UI — SA picker, format tabs, copy button |
| `frontend/src/routes/_authenticated.api-keys.helpers.tsx` | `/api-keys/helpers` route |
| `services/management/internal/handler/registry_info.go` | `GET /api/v1/registry-info` — hostname source |
| `services/management/internal/handler/handler.go` | Registers the auth-gated registry-info route + holds `platformHost` |
| `services/auth/internal/handler/http_service_accounts.go` | SA CRUD + key issue/list/revoke routes; server-side SA-name regex |
| `services/auth/internal/service/service_account.go` | `IssueKey` — 64-hex secret, argon2id hash, one-time raw return |
| `services/auth/internal/handler/http.go` (`parseAPIKeyBearer`) | Parses the `key.<uuid>.<secret>` Bearer form (FUT-006) |

---

## 8. Limitations & notes

- **The renderer is intentionally dumb.** It substitutes two strings and
  sanitises one of them; it does not validate that the SA has push/pull scope,
  that the key exists, or that the hostname resolves. Those are the operator's
  responsibility.
- **No secret rotation surface here.** The helpers page shows *how* to use a
  key, not how to rotate it — rotation lives with the service-account key
  lifecycle (see [`WORKLOAD-IDENTITY.md`](WORKLOAD-IDENTITY.md)).
- **Clipboard copy fails silently** in a non-secure (non-HTTPS) browser
  context — the snippet is still visible for manual selection.

---

> **Last updated:** see `git log -- docs/CREDENTIAL-HELPERS.md`.
> **Found a gap?** PR welcome — this doc is the canonical reference, so any
> divergence between code and this file is the file's bug.

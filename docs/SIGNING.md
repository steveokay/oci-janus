# Image Signing & Key Management

> Canonical reference for how `registry-signer` works, where the keys live,
> and how to verify a signed image from the dashboard, the CLI, or another
> system. Read this when you're touching anything related to signing —
> backend, frontend, ops, or threat-modeling.

---

## TL;DR

- **Keys live in HashiCorp Vault.** They never appear in code, env vars, or
  on the filesystem. The `services/signer` Go binary only asks Vault to do
  the cryptographic operations on its behalf.
- **Signing is a managed operation.** When the dashboard fires "Sign
  manifest", the BFF forwards it to `signer.SignManifest`, which sends the
  manifest digest to Vault's `transit/sign/registry-signer` endpoint.
  Vault returns an ECDSA-P256 signature; the signer persists the metadata
  and publishes `image.signed`.
- **Verification has two paths.** The dashboard's `Verify now` button calls
  `signer.VerifyManifest` (also Vault-backed). The Cosign CLI works
  independently against the same public key — no BFF, no signer service.
- **Three deployment modes** share one `Signer` interface: Vault dev mode
  (today's local stack), Vault prod cluster (on-prem / hybrid), cloud KMS
  (AWS / GCP / Azure — deferred, see status.md).

---

## 1. Where the keys live

### In dev (local docker-compose)

Vault runs in dev mode as a container alongside the rest of the stack:

```yaml
# infra/docker-compose/docker-compose.yml
vault:
  image: hashicorp/vault:1.15
  environment:
    VAULT_DEV_ROOT_TOKEN_ID: dev-root-token
  cap_add: [IPC_LOCK]
```

Once Vault is healthy, `infra/docker-compose/vault/init.sh` provisions the
signer key, policy, and a scoped token:

```bash
# 1. Enable the Transit engine (where keys live and get used)
vault secrets enable transit

# 2. Create an ECDSA P-256 key named 'registry-signer'.
#    exportable=false means there is literally no Vault API path that
#    returns the private key — it can only be used through transit/sign/*
#    and transit/verify/*.
vault write -f transit/keys/registry-signer \
  type=ecdsa-p256 \
  exportable=false

# 3. Policy grants the signer service ONLY the operations it needs —
#    no key creation, no key deletion, no key export.
vault policy write registry-signer-policy - <<EOF
path "transit/sign/registry-signer/*"    { capabilities = ["update"] }
path "transit/verify/registry-signer/*"  { capabilities = ["update"] }
path "transit/keys/registry-signer"      { capabilities = ["read"] }
EOF

# 4. Issue a token bound to that policy; passed to registry-signer via env
vault token create -policy=registry-signer-policy -orphan -ttl=720h
```

The token is handed to `registry-signer` as `VAULT_TOKEN` in the compose
file. **The signer process can do nothing else against Vault.** It can't
rotate keys, can't list other keys, can't export the private material.
Vault's audit log records every sign/verify call.

> **Key loss caveat:** Vault dev mode keeps everything in memory.
> `docker compose down -v` wipes the key. For local work this is fine —
> just re-run `init.sh` and sign again. **Never deploy dev mode to
> production.**

### In production (Vault prod cluster)

Same `SIGNER_KEY_BACKEND=vault` code path. The differences are
operational — separate Vault cluster, HA Raft storage, sealed root key
held by quorum, audit forwarding, periodic key rotation policy. The
signer doesn't change.

### In the cloud (KMS — deferred)

`services/signer/internal/signing/` exposes a `Signer` interface:

```go
type Signer interface {
    Sign(ctx context.Context, payload []byte) ([]byte, string, error)
    Verify(ctx context.Context, payload, signature []byte, keyID string) error
    PublicKey(ctx context.Context) (crypto.PublicKey, string, error)
}
```

Vault is one implementation (`vault.go`). AWS KMS / GCP KMS / Azure Key
Vault are planned as additional implementations (`SIGNER_KEY_BACKEND=awskms`,
etc.) that slot into the same interface — no caller changes. Status:
DEFERRED in `status.md` because the unit-test surface needs a live cloud
KMS key to validate against.

---

## 2. How `services/signer` uses the key

See `services/signer/internal/signing/vault.go` for the implementation.

### At startup

1. **Fetch the public key once.** `vault read transit/keys/registry-signer`
   returns the public key + version history. Used to derive `key_id` (a
   stable fingerprint of the public key) for response payloads, and cached
   in memory for local verify operations.
2. **Health check.** Failing to reach Vault on startup is a fatal error —
   the signer refuses to start. Avoids a race where the service serves
   broken sign requests for the first few seconds while it figures out the
   key.

### On `SignManifest(tenant_id, repository_name, manifest_digest, signer_id)`

1. Compute the canonical payload to sign (Cosign-format: the manifest
   digest + a Sigstore envelope).
2. Send to `transit/sign/registry-signer` with the payload as `input`.
   Vault returns the ECDSA signature blob.
3. Insert into `signatures` table on registry-signer's Postgres:
   `(manifest_digest, signer_id, key_id, signature_digest, signed_at)`.
   The signature blob itself is content-addressed; the row carries
   metadata only.
4. Optionally write the signature as an OCI artifact alongside the image
   manifest (Cosign convention: `<digest>.sig`).
5. Publish `image.signed` to RabbitMQ so audit + webhook consumers see
   the event.

### On `VerifyManifest(tenant_id, manifest_digest, signer_id)`

1. Look up the signature row by `(tenant_id, manifest_digest, signer_id)`.
2. Reconstruct the original payload.
3. Send to `transit/verify/registry-signer` with payload + signature.
   Vault returns `valid: true|false`. No private key needed for verify —
   Vault uses the public key it already has.
4. Return `{verified, failure_reason?}` to the caller.

### On `ListSignatures(manifest_digest, tenant_id)`

Reads only from Postgres. No Vault round-trip. This is the cheap path the
dashboard uses for the default Signing tab render.

---

## 3. End-to-end flow

What happens when you click **Sign manifest** in the dashboard:

```
┌──────────────┐
│   Browser    │  click "Sign manifest" → SignManifestDialog → POST .../sign
└──────┬───────┘
       │ HTTP   { "signer_id": "registry-signer" }
       ▼
┌──────────────┐
│ registry-mgmt│  validate signer_id (ASCII printable, ≤256 chars)
│   (BFF)      │  resolve org/repo → repo_id (metadata.GetRepositoryByName)
│              │  resolve tag → manifest_digest (metadata.GetTag)
└──────┬───────┘
       │ gRPC (mTLS)   signer.SignManifestRequest
       ▼
┌──────────────┐
│   signer     │  call Vault transit/sign/registry-signer
│              │  insert into signatures table
│              │  publish image.signed → RabbitMQ
└──────┬───────┘
       │ gRPC          signer.SignManifestResponse { signature: {...} }
       ▼
┌──────────────┐
│ registry-mgmt│  publish image.signed (audit + webhook consumers)
│              │  return signatureRecord JSON
└──────┬───────┘
       │ HTTP   { signer_id, key_id, signature_digest, signed_at }
       ▼
┌──────────────┐
│   Browser    │  invalidate signature query → SigningPanel re-renders signed
└──────────────┘
```

The dashboard never sees a private key. The BFF never sees a private key.
Only Vault holds it, accessed through a tightly-scoped token that can only
sign / verify with the one named key.

---

## 4. How to verify a signed image

### From the dashboard (cheap list-only)

1. Navigate to `Repositories → <repo> → <tag> → Signing tab`
2. Read the state pill:
   - **Disabled** — BFF isn't wired to the signer service (`SIGNER_GRPC_ADDR` unset)
   - **Unsigned** — no signatures recorded for this digest
   - **Signed** — one or more `SignatureCard` rows render with `signer_id`, `key_id`, `signature_digest`, `signed_at`

This calls `signer.ListSignatures` only — it confirms a signature *exists*
but doesn't run the cryptographic verify.

### From the dashboard (cryptographic verify on demand)

Same Signing tab. Click **Verify now** in the Actions ribbon. The BFF
fans out one `signer.VerifyManifest` call per signature in parallel
(capped at 16, 5s deadline per signature) and the panel updates with:

- Header rollup: `Verified (3/3)` (green) or `Verify failed (1/3)` (red)
- Per-signature badge: `Verified` / `Failed`
- Failure-reason block on any failed card (e.g. `x509: certificate signed by unknown authority`)

The verify path is opt-in because each `VerifyManifest` walks the
signature blob + cert chain — too expensive to do on every render.

### From the Cosign CLI (independent check)

The same crypto check, bypassing the BFF + signer service entirely. Run
on your laptop:

```bash
# 1. Export the public key from Vault (signer uses the same key)
docker exec docker-compose-vault-1 vault read \
  -field=public_key transit/keys/registry-signer > /tmp/cosign.pub

# 2. Verify the signed image
cosign verify --key /tmp/cosign.pub localhost:8081/dev/alpine:3.20
```

Output on success: cosign prints the verified signature blob (JSON with
the manifest digest + signing time). On failure: `no matching signatures`
or `cryptographic verification failed`.

This is the gold-standard check — it doesn't trust the dashboard, the
BFF, or even the signer service. It walks the cosign artifact straight
from the registry and validates against the raw public key.

### Quick end-to-end smoke test

```
1. Sign:    Signing tab → Sign manifest → submit (default `registry-signer`)
2. Read:    Signing tab flips to Signed; one card renders
3. Verify:  Verify now → card gains a green Verified badge
4. Cosign:  Run the CLI command above for an independent confirmation
```

---

## 5. Inspecting & rotating the dev key

### Read the public key + version history

```bash
docker exec docker-compose-vault-1 vault read transit/keys/registry-signer
```

You'll see:
- `type`: `ecdsa-p256`
- `keys`: a map of version → public key (one entry per rotation)
- `latest_version`: the version currently used for new signatures
- `min_decryption_version`: the oldest version still trusted to verify

### Rotate the key

```bash
docker exec docker-compose-vault-1 vault write -f transit/keys/registry-signer/rotate
```

After rotation:
- New `SignManifest` calls use the new key (a new `key_id`)
- Existing signatures keep verifying — `transit/verify` uses whichever
  version signed each blob, looked up from the embedded key version
- Cosign-style key history works automatically

### Manually inspect a signature

```bash
# Get the signature blob from the signer's Postgres
docker exec docker-compose-postgres-1 psql -U registry -d registry_signer \
  -c "SELECT signer_id, key_id, signed_at FROM signatures \
      WHERE manifest_digest = 'sha256:c64c687c...' LIMIT 5;"
```

---

## 6. Threat model & what signing does **not** guarantee

A green **Verified** badge means: *somebody with access to the
`registry-signer` private key signed this manifest digest at the listed
time*. It does **not** guarantee:

| Not guaranteed | What you'd actually need |
|---|---|
| **Who** signed it — `signer_id` is a free-form string chosen by the caller | Per-identity signing policy with attestations (Cosign keyless + Fulcio) |
| **When** it was signed relative to image creation | Signed timestamps via a trusted timestamp authority |
| **The image should be deployed** | A policy gate (`FE-API-018 scan-policies` is the future home) |
| **The image wasn't tampered with after signing** | A scan-and-sign-as-one-step CI job that signs the same digest immediately after pushing it |
| **Other signers approve too** | Multi-signer policy (`require ≥2 signatures by distinct signer_ids`) — backend hook not yet wired |

For the dev environment, treat "signed + verified" as proof-of-concept.
For production trust decisions, layer Cosign verification into your
admission controller (Sigstore Policy Controller for Kubernetes, OPA
Gatekeeper, or your own webhook).

---

## 7. File reference

| File | Why it exists |
|---|---|
| `services/signer/internal/signing/signer.go` | The `Signer` interface (`Sign` / `Verify` / `PublicKey`) all backends implement |
| `services/signer/internal/signing/vault.go` | Vault Transit implementation |
| `services/signer/internal/signing/vault_test.go` | 4 tests against a `httptest` Vault fake — no live Vault needed |
| `services/signer/migrations/*.sql` | `signatures` table schema |
| `proto/signer/v1/signer.proto` | `SignManifest` / `VerifyManifest` / `ListSignatures` RPC definitions |
| `services/management/internal/handler/signature.go` | `GET .../signature` HTTP route — wraps `ListSignatures` + opt-in `verify=true` (FE-API-003 + FE-API-025) |
| `services/management/internal/handler/sign_manifest.go` | `POST .../sign` HTTP route — calls `SignManifest` (FE-API-026) |
| `frontend/src/components/tags/signing-panel.tsx` | Dashboard UI — the Signing tab |
| `frontend/src/components/tags/sign-manifest-dialog.tsx` | Dashboard sign dialog |
| `frontend/src/lib/api/signature.ts` | TanStack Query hooks for the signature endpoints |
| `infra/docker-compose/vault/init.sh` | Provisions the dev key + policy + token on stack startup |
| `infra/docker-compose/docker-compose.yml` | Vault service definition + `SIGNER_KEY_BACKEND=vault` env vars |
| `infra/runbooks/notary-root-key-ceremony.md` | Future TUF / Notary v2 key ceremony (deferred) |

---

## 8. Signed-image admission (futures.md Tier 1 #3)

A signed image only enforces supply-chain trust if pulls of *unsigned*
images get blocked. Setting `repositories.require_signature = TRUE` flips
that gate on at the repo level:

```
oci client → registry-core GET /v2/<org>/<repo>/manifests/<ref>
   → metadata.GetRepository  → require_signature?
       false → return manifest as usual
       true  → signer.ListSignatures(manifest_digest)
                 empty → 403 DENIED
                          body: "repository requires a signed manifest;
                                 sign the image or turn require_signature off"
                 non-empty → return manifest as usual
```

**Operator workflow (container image):**

```
# 1) Push the image (still allowed even on a require_signature repo —
#    the gate is on the PULL side so CI can push, sign, then promote).
docker push registry.local/acme/api:v1.2.3

# 2) Sign with cosign (or via POST .../sign from the dashboard).
cosign sign registry.local/acme/api:v1.2.3

# 3) Flip the flag (only after step 2 succeeds for every digest
#    currently in the repo, or pulls of older tags will start failing).
curl -X PATCH https://api.example.com/api/v1/repositories/acme/api \
     -H "Authorization: Bearer $JWT" \
     -d '{"require_signature": true}'
```

**Operator workflow (Helm chart):**

Both gates (tag immutability + signed-image admission) sit on the OCI
distribution layer, so they apply to Helm charts pushed via
`helm push oci://...` the same way they apply to container images. The
charts go through `services/core`'s `PutManifest` / `GetManifest`
exactly like Docker images do; the artifact type doesn't change the
admission code path. Verified live with the smoke matrix in PR #27.

```
# 1) Login to the OCI registry. --plain-http is only needed against
#    the local dev gateway (HTTP); production / custom-domain hosts
#    served over HTTPS drop the flag.
helm registry login registry.local -u <user> --plain-http

# 2) Push a chart. helm appends the chart name to the URL, so the
#    target is `oci://<host>/<org>` and the chart lands at
#    `<host>/<org>/<chart-name>`.
helm push my-chart-0.1.0.tgz oci://registry.local/acme --plain-http

# 3) Sign the chart's manifest digest via the dashboard API. cosign
#    sign also works against the same digest if you have the CLI
#    installed; both routes write to the shared `signatures` table.
curl -X POST https://api.example.com/api/v1/repositories/acme/my-chart/tags/0.1.0/sign \
     -H "Authorization: Bearer $JWT" \
     -d '{"signer_id": "ci-bot"}'

# 4) Flip the flag — same PATCH as the image workflow.
curl -X PATCH https://api.example.com/api/v1/repositories/acme/my-chart \
     -H "Authorization: Bearer $JWT" \
     -d '{"require_signature": true}'

# 5) Pull / install. Both go through the admission gate; unsigned
#    manifests fail with the same `403 DENIED` body as docker pull.
helm pull oci://registry.local/acme/my-chart --version 0.1.0 --plain-http
helm install my-release oci://registry.local/acme/my-chart --version 0.1.0 --plain-http
```

The Settings tab toggle in the dashboard does step (4) for you. The
Pull / Install snippets shown next to a Helm repo include all of
steps (1) + (5) so an operator can copy-paste straight from the UI.

**Posture:** fail OPEN on metadata or signer reachability blips (warn +
continue) so a transient outage doesn't break every pull. Fail CLOSED on
"flag is on AND zero signatures recorded" — that's the deliberate
contract. If `SIGNER_GRPC_ADDR` is unset at boot, registry-core logs a
startup warning and allows all pulls regardless of the flag (dev-stack
convenience; production deployments always set this).

**Phase 1 contract:** ANY signature passes. A per-repo trusted-key
allowlist is a planned Phase 2 follow-up — until then, an operator who
flips the flag on must also lock down which Cosign identities can sign
for the org (typically via Fulcio OIDC issuer claims, not enforced
here).

**Dashboard:** the toggle lives on the repo Settings tab as
`Signed-image admission` next to `Tag immutability` (both are
security-relevant repo-wide flips with the same shape, but they
compose independently — signed+mutable and unsigned+immutable are
both valid combinations).

**Files:**

| File | Why it exists |
|---|---|
| `services/metadata/migrations/00015_repository_require_signature.sql` | Adds the column with `DEFAULT FALSE` |
| `proto/metadata/v1/metadata.proto` | `Repository.require_signature` field + `UpdateRepositorySignaturePolicy` RPC |
| `services/core/internal/service/registry.go` (`checkSignatureAdmission`) | The fail-OPEN-on-blip gate called from `GetManifest`/`HeadManifest` |
| `services/core/internal/service/errors.go` (`ErrSignatureRequired`) | Sentinel error mapped to 403 DENIED |
| `services/management/internal/handler/handler.go` (`updateRepositoryBody.RequireSignature`) | BFF PATCH plumbing — `*bool` nil-check so unrelated PATCHes don't reset the flag |
| `frontend/src/components/repositories/repo-signature-policy-section.tsx` | Settings-tab card with toggle + explainer |

---

## 9. Related decisions & open work

- **status.md Decision #14** — chose Vault dev mode for local development;
  same `SIGNER_KEY_BACKEND=vault` path used in production
- **status.md Decision #6** — chose Cosign + Notary v2; only Cosign is
  implemented today, Notary v2 deferred
- **status.md REM-001** — scanner plugin sandboxing (unrelated but shares
  the "external-process trust" theme)
- **FE-API-025 (DONE)** — `?verify=true` opt-in on the signature route
- **FE-API-026 (DONE)** — `POST .../sign` from the dashboard
- **DEFERRED** — Cloud KMS backends (AWS / GCP / Azure)
- **DEFERRED** — Notary v2 / TUF signing path (separate from Cosign)
- **DEFERRED** — Sigstore keyless signing (Fulcio + Rekor)
- **NOT STARTED** — Per-tenant signing policy (`require signed images
  before deploy`); planned as part of FE-API-018 scan-policies

---

> **Last updated:** see `git log -- docs/SIGNING.md`.
> **Found a gap?** PR welcome — this doc is the canonical reference, so
> any divergence between code and this file is the file's bug.

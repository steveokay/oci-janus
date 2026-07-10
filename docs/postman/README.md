# Postman collection — registry-management

A Postman v2.1 collection covering the registry's HTTP API: every route the
`services/management` REST BFF exposes, plus a curated **`identity`** folder
with the core `registry-auth` routes you need to obtain a token.

## Files

| File | Purpose |
|---|---|
| `registry-management.postman_collection.json` | The collection — one folder per resource group. **Generated — do not hand-edit.** |
| `registry-management.postman_environment.json` | Local-dev environment (base URL, dev tenant id, seeded creds, captured token). |

## This collection is generated

The collection is generated from [`../openapi.json`](../openapi.json), which is
itself generated from the `services/management` route table. Every BFF endpoint
is therefore covered and the collection can never drift from the handlers.

- Regenerate: `cd services/management && make openapi` (runs `openapi-gen` then
  `postman-gen`).
- CI enforces freshness: the `openapi` job in `.github/workflows/ci-management.yml`
  regenerates both artifacts and fails if the committed copies are stale.
- To change what the collection contains, edit the generator
  (`services/management/cmd/postman-gen/main.go`) — not this JSON.

The one hand-maintained piece is the `identity` folder: those routes live in
`registry-auth`, not the BFF, so they are not in `openapi.json`. They are a
small, stable, curated set (`curatedIdentityOps` in the generator) — above all
so you can log in first.

## Quickstart

1. **Bring up the stack:**
   ```bash
   cd infra/docker-compose && docker compose up -d
   ```
2. **Import both JSON files into Postman.** (File → Import → drop both.)
3. **Select the `registry-management — local dev` environment** (top-right).
4. **Run `identity → POST /api/v1/login`.** The default body uses
   `{{username}}` / `{{password}}` / `{{tenantID}}` from the environment. Its
   test script captures the returned JWT into `{{token}}`, so every subsequent
   request authenticates automatically.
   - If the response is `mfa_required`, the script captures `{{challenge_token}}`
     instead — complete `identity → POST /api/v1/login/mfa` to get the JWT.
5. **Run any other request** — they inherit collection-level Bearer auth from
   `{{token}}`. Public routes (e.g. login, `GET /api/v1/auth/providers`)
   override this with no-auth.

## Auth model

`{{token}}` may be either an RS256 JWT (from login) or a
`key.<uuid>.<secret>` API key. Issue an API key from
`identity → POST /api/v1/apikeys` for CI/bot use.

# Postman collection — registry-management

A Postman v2.1 collection exercising every public HTTP endpoint exposed by
the registry: the full `services/management` REST API plus the auth-side
routes the gateway exposes under the same `/api/v1` prefix.

## Files

| File | Purpose |
|---|---|
| `registry-management.postman_collection.json` | The collection — folders per resource group. |
| `registry-management.postman_environment.json` | Environment variables (base URL, tenant id, captured token, etc.). |

## Endpoint coverage

| Folder | Endpoints |
|---|---|
| Health | `GET /healthz` |
| Auth | `POST /api/v1/login`, `POST /api/v1/logout`, `POST /api/v1/token/refresh`, `POST /api/v1/users` |
| API Keys | `GET/POST /api/v1/apikeys`, `DELETE /api/v1/apikeys/{id}` |
| Stats | `GET /api/v1/stats` |
| Repositories | `GET/POST /api/v1/repositories`, `GET/DELETE /api/v1/repositories/{org}/{repo}` |
| Tags | `GET /api/v1/repositories/{org}/{repo}/tags`, `DELETE …/tags/{tag}` |
| Scans | `GET/POST …/tags/{tag}/scan` |
| Builds | `GET …/tags/{tag}/builds` |
| RBAC — org members | `GET/POST /api/v1/orgs/{org}/members`, `DELETE …/members/{assignmentID}` |
| RBAC — repo members | `GET/POST /api/v1/repositories/{org}/{repo}/members`, `DELETE …/members/{assignmentID}` |
| Admin — tenants (platform-admin) | `GET/POST /api/v1/admin/tenants`, `GET/DELETE /api/v1/admin/tenants/{tenantID}`, `PUT /api/v1/admin/tenants/{tenantID}/quota` |
| Webhooks (FE-API-021..024) | `GET/POST /api/v1/webhooks`, `PATCH/DELETE /api/v1/webhooks/{id}`, `GET /api/v1/webhooks/{id}/deliveries`, `POST /api/v1/webhooks/{id}/test`, `POST /api/v1/webhooks/{id}/rotate-secret` |

## Quickstart

1. **Bring up the stack:**
   ```bash
   cd infra/docker-compose && docker compose up -d
   ```
2. **Import both JSON files into Postman.** (File → Import → drop both.)
3. **Select the `registry-management — local dev` environment** in the top-right.
4. **Run `Auth → POST /api/v1/login`** with the seeded creds (default body uses
   `admin` / `Admin1234!dev` against the dev tenant). The collection's test
   script writes the returned JWT into `{{token}}`, so every subsequent
   request authenticates automatically.
5. **Run any other request** — they all inherit collection-level Bearer auth
   from `{{token}}`.

## Captured variables

The login script also captures `{{userId}}` and `{{tenantId}}` so the
RBAC-grant requests work without manual edits. The webhook create script
captures `{{webhookId}}` for the follow-up update / delete / test / rotate
calls. The API key create script captures `{{apiKeyId}}`.

## Updating the collection

There is no codegen — the JSON is hand-maintained. When you add a new HTTP
route to `services/management` (or to `services/auth`'s HTTP surface), add
a matching request to the appropriate folder here. Keep the per-folder
ordering of `GET → POST → PATCH → DELETE → side routes` so the file is
easy to scan.

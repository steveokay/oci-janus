[//]: # (FE-API-034 — SAML SP setup reference. Pair this with docs/SERVICES.md §2 for the auth service surface.)

# SAML Single Sign-On — Setup Guide

> Canonical reference for configuring SAML SSO against `registry-auth`. Read
> this when wiring a new tenant to an enterprise IdP (Okta, Azure AD / Entra
> ID, Google Workspace, Auth0, ADFS, OneLogin, JumpCloud, …) or when
> debugging a stuck `/auth/saml/.../acs` round trip.
>
> For the OAuth / OIDC flow (Google / GitHub / Microsoft personal +
> generic OIDC), see the `SSO` section of [`docs/SERVICES.md` §2](SERVICES.md#2-registry-auth).
> This doc covers SAML 2.0 only.

---

## TL;DR

- **One SP keypair, many IdPs.** `registry-auth` presents a single
  `SAML_SP_CERT_PATH` / `SAML_SP_KEY_PATH` to every configured IdP. Per-tenant
  configuration lives in the `auth_providers` table.
- **Routes:**
  - `GET /auth/saml/{provider_id}/start` — kicks off SP-initiated auth.
  - `POST /auth/saml/{provider_id}/acs` — receives the IdP's signed Response.
- **Login session state** uses the same `auth_login_sessions` table as OAuth.
  `RelayState` = the SAML term for our single-use `state` token (10-minute TTL).
- **Auto-provisioning** reuses `EnsureSSOUser`. SAML carries no
  `email_verified` claim — we treat IdP authentication as proof and pass
  `EmailVerified: true`.
- **No SAML support** when `SAML_SP_CERT_PATH` or `SAML_SP_KEY_PATH` is
  unset — the routes return `501 NOTCONFIGURED` so the dashboard can fall
  back to "SAML coming soon" without parsing error bodies.

---

## 1. Architecture

```
                ┌──────────────────────────────────────────────┐
                │              browser (user)                  │
                └────────────────────┬─────────────────────────┘
                                     │ 1. GET /auth/saml/{id}/start?next=/dashboard
                ┌────────────────────▼─────────────────────────┐
                │              registry-auth                   │
                │                                              │
                │  • LookupProviderByID                        │
                │  • saml.BuildServiceProvider(metadata, …)    │
                │  • sp.MakeAuthenticationRequest()            │
                │  • CreateSAMLLoginSession(authnReq.ID)       │
                │       └─► auth_login_sessions row            │
                │           (state=RelayState, pkce_verifier=  │
                │            AuthnRequest ID, expires +10 min) │
                │  • 302 → IdP SSO URL                         │
                └────────────────────┬─────────────────────────┘
                                     │ 2. SAML AuthnRequest (HTTP-Redirect, signed)
                ┌────────────────────▼─────────────────────────┐
                │            Enterprise IdP                    │
                │  (Okta / Entra ID / Google / Auth0 / …)      │
                │                                              │
                │  • Verifies our SP signature against the     │
                │    cert in SP metadata.                      │
                │  • Authenticates the user (MFA, etc).        │
                │  • Builds + signs the SAMLResponse.          │
                │  • 200 OK with HTML auto-POST form back to   │
                │    our ACS URL.                              │
                └────────────────────┬─────────────────────────┘
                                     │ 3. POST /auth/saml/{id}/acs
                                     │     (SAMLResponse, RelayState)
                ┌────────────────────▼─────────────────────────┐
                │              registry-auth                   │
                │                                              │
                │  • ConsumeLoginSession(RelayState)           │
                │       └─► row deleted atomically             │
                │           → replay = 400 INVALIDSTATE        │
                │  • sp.ParseResponse(r, [authnReq.ID])        │
                │       • XML signature                        │
                │       • Conditions / NotOnOrAfter            │
                │       • Audience                             │
                │       • InResponseTo == our stored ID        │
                │  • ExtractAttribute(email, name) →           │
                │    SSOIdentity{EmailVerified:true}           │
                │  • EnsureSSOUser → auto-provision if needed  │
                │  • IssueSSOToken (JWT)                       │
                │  • 302 → {next}?sso_token=<jwt>              │
                └──────────────────────────────────────────────┘
```

---

## 2. SP keypair lifecycle

The SP signing certificate + key are **process-wide** — every configured
SAML provider shares the same keypair. Per-tenant rotation is intentionally
not supported in v1 (each tenant would have to re-upload SP metadata to
its IdP); see §9 for the rotation runbook when the process-wide pair
expires.

### 2.1 Dev (local docker-compose)

1.  Generate a self-signed cert + key with a one-year validity window:

    ```bash
    openssl req -x509 -newkey rsa:2048 -nodes \
      -keyout saml-sp.key -out saml-sp.crt \
      -days 365 \
      -subj "/CN=registry-auth-saml-sp"
    chmod 600 saml-sp.key
    ```

2.  Mount them into the `registry-auth` container and point the env vars
    at the mount paths:

    ```env
    # services/auth/.env
    SAML_SP_CERT_PATH=/etc/registry-auth/saml-sp.crt
    SAML_SP_KEY_PATH=/etc/registry-auth/saml-sp.key
    ```

3.  Restart `registry-auth`. The startup log should print
    `SAML SP keypair loaded — /auth/saml/... routes active`.

### 2.2 Production (Kubernetes)

Mint the keypair via cert-manager with an internal CA issuer (same model
as the mTLS certs — see [CLAUDE.md §7](../CLAUDE.md#7-authentication--security)).
Mount the resulting `Secret` as files under `/etc/registry-auth/`. The
`SAML_SP_CERT_PATH` + `SAML_SP_KEY_PATH` env vars come from the same
ConfigMap that drives mTLS.

Rules:

- **Cert validity:** maximum 365 days. The IdP-side trust chain is the
  raw SP cert bytes — rotation is a coordinated event with the customer's
  IdP admin.
- **Key file permissions:** `chmod 600` (CLAUDE.md §7, SEC-024).
- **No SP encryption support in v1.** IdPs that require encrypted
  assertions are not supported — almost every enterprise IdP defaults to
  signed-but-unencrypted, so this is rarely a blocker. Tracked as a v2
  follow-up.

### 2.3 Format requirements

`saml.LoadSPConfig` accepts:

- **Certificate:** PEM `CERTIFICATE` block (X.509).
- **Private key:** PEM `RSA PRIVATE KEY` (PKCS1) **or** `PRIVATE KEY`
  (PKCS8). EC keys are rejected — crewjam/saml signs with RSA only.

A misformatted block causes `registry-auth` to fail startup with a clear
error. The service refuses to run with only the cert or only the key set
(prevents accidental half-configurations).

---

## 3. Creating a SAML provider

> Most operators do this via the dashboard (Admin → Authentication →
> Add provider → SAML). The HTTP surface is documented below for CLI /
> Terraform users.

### 3.1 Required fields

| Field | Notes |
|---|---|
| `tenant_id` | The tenant that owns this provider. |
| `type` | Must be `saml`. |
| `display_name` | Shown on the dashboard's sign-in page. Max 128 chars. |
| `saml_idp_metadata_xml` | The full IdP metadata XML document (see §4). |
| `enabled` | `true` to show the button on the sign-in page. |
| `auto_provision` | `true` to create local users on first SAML login. |
| `default_role` | `reader` / `writer` / `admin` / `owner`. Granted at `org:*` scope. |

### 3.2 Optional fields

| Field | Default | Notes |
|---|---|---|
| `saml_entity_id` | `{SSO_BASE_URL}/auth/saml/metadata` | SP EntityID — set this if the IdP requires a specific URI. |
| `saml_audience` | (falls back to EntityID) | Distinct Audience restriction — most IdPs don't need this. |

### 3.3 Admin API

```bash
# POST /api/v1/admin/auth-providers
curl -X POST https://registry.example.com/api/v1/admin/auth-providers \
  -H "Authorization: Bearer $JWT" \
  -H "Content-Type: application/json" \
  -d '{
    "tenant_id": "00000000-0000-0000-0000-000000000001",
    "type": "saml",
    "display_name": "Sign in with Acme SSO",
    "enabled": true,
    "saml_idp_metadata_xml": "<EntityDescriptor>…</EntityDescriptor>",
    "auto_provision": true,
    "default_role": "reader"
  }'
```

The response includes the generated `id` — use that in the ACS / SSO URLs
you give the IdP admin (§4.2).

---

## 4. Wiring the IdP

### 4.1 Fetch the IdP metadata

Every supported IdP exposes a metadata URL. Save the document as XML and
paste it into `saml_idp_metadata_xml` (or upload via the dashboard).

| IdP | Metadata URL shape |
|---|---|
| Okta | `https://<org>.okta.com/app/<app_id>/sso/saml/metadata` |
| Azure AD / Entra ID | `https://login.microsoftonline.com/<tenant>/federationmetadata/2007-06/federationmetadata.xml?appid=<app>` |
| Google Workspace | Download from Admin Console → Apps → Web and mobile apps → your app → SAML setup. |
| Auth0 | `https://<tenant>.auth0.com/samlp/metadata/<client_id>` |
| OneLogin | `https://<org>.onelogin.com/saml/metadata/<app_id>` |
| ADFS | `https://<adfs-host>/FederationMetadata/2007-06/FederationMetadata.xml` |

The metadata document is the **source of truth** for the IdP's signing
certificate, SSO endpoint, NameID format, and bindings. We re-parse it
on every request, so an admin PATCH to update the metadata takes effect
immediately (no cache invalidation needed — see §9 cert rotation).

### 4.2 Tell the IdP about our SP

Configure the IdP application with these values (substitute the provider
ID returned by the create call):

| IdP field | Our value |
|---|---|
| **ACS / Assertion Consumer Service URL** | `{SSO_BASE_URL}/auth/saml/{provider_id}/acs` |
| **EntityID / Audience URI** | Default: `{SSO_BASE_URL}/auth/saml/metadata`<br>Or whatever you set in `saml_entity_id`. |
| **Binding** | HTTP-POST for ACS, HTTP-Redirect for SSO. |
| **NameID format** | We accept any format; emailAddress is recommended. |
| **Signed AuthnRequest?** | Yes — we sign with RSA-SHA256. Upload our SP cert (`saml-sp.crt`) to the IdP. |
| **Sign assertion?** | Yes — required. We verify the assertion signature. |
| **Encrypt assertion?** | **No** — not supported in v1. |

### 4.3 Required SAML attributes

The ACS handler extracts user attributes in priority order. As long as
**at least one of** the email candidates resolves, login works. If none
of the email attributes is present we fall back to `Subject.NameID.Value`,
which works when the IdP uses the `emailAddress` NameID format.

**Email candidates** (first match wins):

1. `http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress` (ADFS / Azure AD)
2. `urn:oid:0.9.2342.19200300.100.1.3` (LDAP `mail` OID)
3. `email`
4. `mail`
5. `emailAddress` / `EmailAddress`

**Display name candidates** (first match wins):

1. `http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name`
2. `http://schemas.xmlsoap.org/ws/2005/05/identity/claims/displayname`
3. `urn:oid:2.16.840.1.113730.3.1.241`
4. `urn:oid:2.5.4.3`
5. `name` / `displayName` / `DisplayName` / `cn`

If your IdP uses different attribute names, either add a mapping in
the IdP's claim transformation UI, or extend the candidate lists in
`services/auth/internal/handler/saml.go`.

---

## 5. End-to-end flow

The HTTP round trip for SP-initiated login:

```
1.  Browser → GET /auth/saml/<id>/start?next=/dashboard
        ↓ 302
2.  Browser → GET https://idp.example.com/sso?SAMLRequest=…&RelayState=…
        ↓ user signs in at IdP (MFA, etc.)
        ↓ 200 OK with HTML auto-POST form
3.  Browser → POST /auth/saml/<id>/acs
                body: SAMLResponse=…&RelayState=…
        ↓ 302
4.  Browser → GET /dashboard?sso_token=<jwt>
        ↓ frontend swaps token into authStore + strips it from URL
5.  Logged in.
```

**Replay defence:** the `auth_login_sessions` row is deleted atomically
by `ConsumeByState` on step 3. A second POST with the same `RelayState`
returns `400 INVALIDSTATE` — even if the SAMLResponse signature is
still valid.

**Response binding defence:** `ParseResponse` is called with the stored
`AuthnRequest.ID` as the only permitted `InResponseTo`. An attacker who
captures a `RelayState` cannot pair it with a Response from a different
in-flight SP-initiated flow.

---

## 6. Username derivation

Auto-provisioned users get a deterministic username from the email
(`DeriveSSOUsername` in `services/auth/internal/service/sso.go`):

1. Take the local part (before `@`).
2. Replace non-`[a-zA-Z0-9_-]` chars with `-`.
3. Trim to 56 chars.
4. Append a 6-char base64url hash of the full email (so
   `alice@example.com` and `alice@other.com` don't collide).

A racing parallel SAML callback gets the same email through
`GetByEmail` — `CreateSSOUser` returning `ErrAlreadyExists` triggers a
re-query so both callers end up on the same user row.

---

## 7. Audit trail

| Event | Routing key | When |
|---|---|---|
| `auth.provider_created` | admin POST creates a SAML provider |
| `auth.provider_updated` | admin PATCH (metadata refresh, enable/disable) |
| `auth.provider_deleted` | admin DELETE |
| `auth.user_sso_provisioned` | first SAML login auto-creates a local user |

Payloads never contain SP key material, SAMLResponse XML, or the JWT —
only IDs + the IdP-supplied subject identifier.

---

## 8. Troubleshooting

### `400 INVALIDSAML — SAML response failed validation`

Check the server log; the underlying cause is in the `cause=` field:

| `cause=` substring | Diagnosis |
|---|---|
| `cannot parse base64` | The IdP posted a non-base64 SAMLResponse. Usually a mis-wired ACS URL. |
| `InResponseTo does not match` | Either our login_sessions row was lost (cleanup raced) or the IdP echoed the wrong AuthnRequest ID. Usually transient — ask the user to retry. |
| `signature verification failed` | The IdP rotated its signing cert. Re-fetch metadata + PATCH the provider. |
| `cannot find SignatureMethod` | The IdP did not sign the assertion. Enable "Sign assertion" in the IdP app config. |
| `assertion expired` | Clock skew. Check NTP on both sides — we use the system clock, no skew tolerance. |
| `Audience does not match` | The IdP issued the assertion for a different EntityID. Verify `saml_entity_id` matches the IdP app config. |

### `400 INVALIDSTATE — invalid or expired RelayState`

- The RelayState was reused (replay defence working as intended).
- The user took more than 10 minutes at the IdP login screen.
- The `auth_login_sessions` row was swept by the background cleanup loop
  (runs every 60 s, deletes rows past `expires_at`).

The user should restart the flow from the dashboard sign-in page.

### `400 MISSINGEMAIL — SAML assertion has no email`

No matching email attribute and `Subject.NameID` was empty. Add an email
claim mapping in the IdP application config.

### `401 UNAUTHORIZED — user does not exist and auto-provision is disabled`

`auto_provision: false` on the provider row. Either flip it to `true` or
have an admin create the user first (then re-attempt the SAML login).

### `501 NOTCONFIGURED — SAML SP cert/key not configured`

`SAML_SP_CERT_PATH` / `SAML_SP_KEY_PATH` is empty. See §2.

---

## 9. Cert rotation runbook

When the SP signing cert is within 30 days of expiry:

1. **Mint** a new keypair (cert-manager will do this automatically in
   prod; dev uses the openssl command in §2.1).
2. **Deploy** the new cert + key files to every `registry-auth` replica.
3. **Restart** the pods to pick up the new keypair (`registry-auth` does
   not hot-reload the SP keypair in v1 — tracked as a follow-up).
4. **Notify** every customer that's configured a SAML provider against
   us. Each must:
   - Download our new SP cert from `{SSO_BASE_URL}/auth/saml/metadata`
     (endpoint **not yet implemented** in v1 — for now, hand-deliver the
     cert file).
   - Re-upload it in their IdP application config.
5. **Watch** the `saml: ParseResponse failed cause=signature verification failed`
   metric for spikes — that signals an IdP that hasn't rotated yet.

To rotate **without** breaking existing IdPs, generate a fresh cert from
the same RSA key (`openssl req -new -key saml-sp.key -x509 -days 365 …`).
Same public key, new validity window — IdPs that pin only the public key
keep working; IdPs that pin the full cert bytes still need a re-upload.

---

## 10. v1 limitations & follow-ups

| Limitation | Workaround | Tracked |
|---|---|---|
| No SAML response encryption | Use signed-only assertions (the default for ~all IdPs). | v2 |
| No SP metadata endpoint (`/auth/saml/metadata`) | Hand-deliver `saml-sp.crt`. | v2 |
| No SAML Single Logout (SLO) | Local logout only (revokes our JWT, leaves IdP session open). | v2 |
| No IdP-initiated flow | Users must start at our sign-in page. | v2 |
| No per-tenant SP keypair | One process-wide keypair across all tenants. | Deferred — adds significant ops complexity for negligible security gain. |
| No hot-reload of SP keypair | Pod restart on rotation. | Backlog |
| Metadata cached only per-request | Acceptable — re-parse cost is sub-millisecond. | Reassess if benchmarks change. |

See `status.md` for sprint scheduling.

---

## 11. Reference: code map

| Concern | File |
|---|---|
| SP construction, attribute extraction | `services/auth/internal/saml/sp.go` |
| HTTP handlers (`start` + `acs`) | `services/auth/internal/handler/saml.go` |
| Login session helpers | `services/auth/internal/service/sso.go` (`CreateSAMLLoginSession`, `ConsumeLoginSession`) |
| Admin CRUD | `services/auth/internal/handler/sso_admin.go` |
| Repository | `services/auth/internal/repository/auth_providers.go`, `login_sessions.go` |
| Migrations | `services/auth/migrations/2026062100000?_*.sql` |
| Env vars | `services/auth/.env.example` (search "SAML SP keypair") |
| Tests | `services/auth/internal/handler/saml_test.go` |

---

> **Last updated:** see Git log.
> **Questions?** Open an issue with the label `auth-sso` and CC the
> Auth squad.

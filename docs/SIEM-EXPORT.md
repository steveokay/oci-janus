# SIEM-EXPORT.md — Audit-log streaming reference

> **Audience:** operators wiring an external SIEM (Splunk, QRadar,
> Datadog, Elastic, custom collector) to receive every audit event;
> developers extending the renderer or adding a new format.
>
> **Status:** futures.md Tier 1 #4 Phase 1 shipped 2026-06-23. Three
> formats (syslog RFC 5424, CEF, HTTPS webhook); per-tenant config;
> AES-256-GCM-encrypted shared secret + bearer token; SSRF guard;
> observability counters (`last_success_at`, `last_error`,
> `dlx_depth`). Async DLX queue + replay is Phase 2.

---

## 1. Architecture

```
registry-* services
   │ events.Publish(...)
   ▼
RabbitMQ registry.events exchange
   │ routing key: push.completed / scan.completed / ...
   ▼
services/audit/internal/eventconsumer
   │  ▸ INSERT audit_events  (durable record)
   │  ▸ go dispatcher.dispatch(event)  (fire-and-forget goroutine)
   │       │
   │       ├─ GetAuditExportConfig(tenant_id)
   │       ├─ apply include/exclude filter to event.Action
   │       ├─ AES-256-GCM Open(hmac_secret, bearer_token)
   │       ├─ render: syslog_rfc5424 | cef | webhook
   │       ├─ guardTargetURL (SSRF — block RFC1918/loopback/CGNAT)
   │       ├─ ship:
   │       │     • syslog → TCP / TLS dial + write line
   │       │     • cef    → same as syslog (CEF body over syslog framing)
   │       │     • webhook → POST JSON, X-Signature: sha256=<hmac>
   │       ▼
   │   success                       failure
   │     │                             │ (3 attempts, exponential
   │     │                             │  backoff capped at 5s)
   │     ▼                             ▼
   │  TouchSuccess (updates       TouchFailure (last_error) +
   │  last_success_at)             IncrementDLX (dlx_depth + 1)
   ▼
audit DB (unchanged behaviour for non-streaming tenants)
```

The audit DB INSERT happens FIRST and is the source of truth. Stream
delivery never blocks the consumer's RabbitMQ ACK, so a downstream
SIEM outage cannot cause double-INSERTs into `audit_events`.

---

## 2. Per-tenant configuration

One row per tenant in `audit_export_configs`. CRUD via the management
BFF (which proxies to the AuditService gRPC RPCs):

| Method | Route | Body / Effect |
|---|---|---|
| `GET` | `/api/v1/workspace/me/audit-export` | Returns `{config: {...}}` or `{config: null}` when never configured. Secret material is NEVER returned — only `hmac_secret_set` / `bearer_token_set` booleans. |
| `PUT` | `/api/v1/workspace/me/audit-export` | Upsert. Secret rotation contract: empty string = leave alone, non-empty = rotate, `*_clear: true` = revoke. |
| `DELETE` | `/api/v1/workspace/me/audit-export` | Idempotent. Streaming stops on the next event. |
| `POST` | `/api/v1/workspace/me/audit-export/test` | Fires a synthetic `audit_export.test` event via the same render+ship pipeline. Returns the rendered wire payload + delivery error if any. |

All four routes are workspace-admin gated (`admin`/`owner` on any org
in the tenant). The dashboard surface lives at
`/workspace/audit-export` (linked from sidebar **Integrations**).

---

## 3. Wire formats

### 3.1 syslog_rfc5424

Structured-text line per RFC 5424, framed over TCP or TLS:

```
<109>1 2026-06-23T09:48:58.911Z registry-audit oci-janus - image.signed -
  [oci-janus@53430 tenant_id="…" actor_id="00000000-…" actor_type="system"
   actor_ip="" resource="{\"repo\":\"dev/alpine\",…}" outcome="success"
   event_id="f730d919-…"]
```

- Priority `<109>` = facility 13 (log audit) × 8 + severity 6 (info)
  for `outcome=success` / severity 4 (warning) for `outcome=failure`.
- Hostname = `registry-audit`, app-name = `oci-janus`, msg-id =
  the audit event's `action` (e.g. `image.signed`).
- Structured data block carries every operationally-useful field
  (`tenant_id`, `actor_*`, `resource`, `outcome`, `event_id`) keyed by
  the platform PEN `53430` (placeholder until a real IANA PEN is
  registered).
- Transport: target URL must be `syslog+tcp://host:port` or
  `syslog+tls://host:port`. TLS uses the system trust store; no
  client cert auth in v1.

### 3.2 cef (Common Event Format)

```
CEF:0|oci-janus|registry|1.0|image.signed|image.signed|3|rt=Jun 23 2026 09:48:58.911 src= suser=00000000-… act=image.signed outcome=success cs1Label=tenant_id cs1=98dbe36b-… cs2Label=resource cs2={…} cs3Label=event_id cs3=f730d919-… cs4Label=metadata_b64 cs4=eyJyYXcuLi59
```

- ArcSight-style header (`Vendor|Product|Version|EventID|EventName|Severity|Extensions`).
- Severity 7 (high) on failure, 3 (low/info) on success.
- Custom string fields ride on `cs1`/`cs2`/`cs3`/`cs4` per CEF convention.
- Transport rides on the same syslog dial path — operators point a
  syslog server (e.g. Splunk's syslog collector) at the URL and it
  parses the CEF body downstream.

### 3.3 webhook (HTTPS JSON)

```
POST https://siem.example.com/audit
X-Signature: sha256=<hex(HMAC-SHA256(body, hmac_secret))>
Authorization: Bearer <bearer_token>   # only when no HMAC secret
Content-Type: application/json
User-Agent: oci-janus-audit-export/1.0

{
  "id":          "f730d919-…",
  "tenant_id":   "98dbe36b-…",
  "actor_id":    "00000000-…",
  "actor_type":  "user",
  "actor_ip":    "203.0.113.42",
  "action":      "image.signed",
  "resource":    "{\"repo\":\"dev/alpine\",\"tag\":\"3.18\",…}",
  "outcome":     "success",
  "metadata":    {…},
  "occurred_at": "2026-06-23T09:48:58.911Z"
}
```

- HTTPS-only at the transport layer. `http://` is rejected EXCEPT for
  `http://localhost`, `http://127.0.0.1`, and `http://host.docker.internal`
  — the last is the canonical way to reach the host machine from
  inside the audit container during a dev smoke test.
- HMAC-SHA256 is the preferred auth mode. When `hmac_secret` is set,
  `X-Signature: sha256=<hex>` is added to every request. The
  recipient should reject any request where the signature doesn't
  match. Sample verifier (Go):

  ```go
  func verify(body []byte, sig, secret string) bool {
      mac := hmac.New(sha256.New, []byte(secret))
      mac.Write(body)
      want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
      return hmac.Equal([]byte(sig), []byte(want))
  }
  ```

- Bearer tokens are the fallback for collectors that only accept
  `Authorization: Bearer …`. Set on the config; sent verbatim. HMAC
  wins when both are configured.

---

## 4. Filters

`event_filters_json` (optional) is a JSONB blob:

```json
{
  "include": ["push.completed", "scan.completed", "auth.*"],
  "exclude": ["webhook.*"]
}
```

- Empty / null = ship every event.
- `exclude` wins over `include`.
- Wildcards: trailing `.*` matches any suffix (`auth.*` matches
  `auth.provider_created`, `auth.user_sso_provisioned`).
- Malformed JSON → fail open (ship everything) so a broken filter
  doesn't silently drop events.

---

## 5. Security

### 5.1 Secret-at-rest

`hmac_secret` and `bearer_token` are AES-256-GCM-sealed before
persistence using `AUDIT_EXPORT_SECRETS_KEY_HEX` (64-char hex =
32-byte key). The key never leaves the audit service. The GET RPC
returns `*_set` booleans only — the raw secret never crosses the
wire to the BFF or the FE.

**Key generation (dev):**

```sh
openssl rand -hex 32
# 64 chars; paste into AUDIT_EXPORT_SECRETS_KEY_HEX
```

**Production:** mount via External Secrets Operator → Vault /
AWS Secrets Manager / GCP Secret Manager. Treat as a tenant-wide
escrow key — rotating it means re-PUTting every tenant's secrets.

### 5.2 SSRF guard

`guardTargetURL` runs at every delivery (not just at PUT time —
DNS can shift between writes). Blocks:

- RFC 1918 (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16)
- Loopback (127.0.0.0/8, ::1)
- Link-local (169.254.0.0/16, fe80::/10)
- Carrier-grade NAT (100.64.0.0/10)

Allowlisted: `localhost`, `127.0.0.1`, `::1`, `host.docker.internal`
(dev escape hatches only).

### 5.3 Transport TLS

- `syslog+tls://` requires TLS 1.2+ (system trust store).
- `https://` clients enforce TLS 1.2+ with `ResponseHeaderTimeout: 5s`
  and `TLSHandshakeTimeout: 5s` to prevent connection-exhaustion DoS.

---

## 6. Observability

The config row carries operator-facing counters surfaced on the
dashboard:

| Field | Meaning |
|---|---|
| `last_success_at` | UTC timestamp of the most recent ACKed delivery. `null` until the first success. |
| `last_attempt_at` | UTC timestamp of the most recent delivery attempt — success or failure. |
| `last_error` | Truncated (≤512 chars) error string from the last failed attempt. Empty after a subsequent success. |
| `dlx_depth` | Cumulative count of events that exhausted the in-process retry budget and got "parked" (logged + counter bump; see §7). |

The dashboard's `ObservabilityCard` renders the health pill (healthy
when `last_success_at` is recent AND `dlx_depth == 0` AND
`last_error` is empty) and surfaces the last error string for
operator troubleshooting.

---

## 7. v1 retry posture (and Phase 2 plans)

v1 ships **in-process retry only**: `MaxAttempts = 3`, exponential
backoff capped at 5s (`1s → 2s → 4s`). After exhaustion the dispatcher
calls `TouchAuditExportFailure` + `IncrementAuditExportDLX(1)` and
returns. The audit event STAYS in the local audit DB (the source of
truth) but is not re-attempted automatically.

**Phase 2 (deferred):** promote dispatch to a separate RabbitMQ
queue with proper DLX semantics — `audit.export.<format>` queue per
format, `dlx.audit-export` on exhaustion, an admin "drain" action
on the dashboard that replays from the DLX after the operator
fixes their SIEM. The MVP closes the procurement gate without
committing to the queue infrastructure up front; the design above
is intentionally additive (the in-process path stays valid as a
"fast path" once the queue lands).

---

## 8. Smoke test

```sh
# 1) Spin up a local HMAC-verifying receiver on the host
cat > /tmp/receiver.go <<'EOF'
package main
import (
    "crypto/hmac"; "crypto/sha256"; "encoding/hex"
    "fmt"; "io"; "net/http"
)
const secret = "smoke-test-secret"
func main() {
    http.HandleFunc("/audit", func(w http.ResponseWriter, r *http.Request) {
        body, _ := io.ReadAll(r.Body)
        m := hmac.New(sha256.New, []byte(secret))
        m.Write(body)
        ok := r.Header.Get("X-Signature") == "sha256="+hex.EncodeToString(m.Sum(nil))
        fmt.Printf("sig-ok=%t body=%s\n", ok, body)
        w.WriteHeader(http.StatusOK)
    })
    _ = http.ListenAndServe(":19999", nil)
}
EOF
go run /tmp/receiver.go &

# 2) PUT a webhook config pointing at the host
JWT=$(curl -s -X POST http://localhost:8080/api/v1/login \
  -d '{"username":"admin","password":"Admin1234!dev","tenant_id":"98dbe36b-ef28-4903-b25c-bff1b2921c9e"}' \
  | jq -r .token)
curl -s -X PUT http://localhost:8091/api/v1/workspace/me/audit-export \
  -H "Authorization: Bearer $JWT" \
  -d '{"enabled":true,"format":"webhook","target_url":"http://host.docker.internal:19999/audit","hmac_secret":"smoke-test-secret"}'

# 3) Send a test event
curl -s -X POST http://localhost:8091/api/v1/workspace/me/audit-export/test \
  -H "Authorization: Bearer $JWT"

# 4) Trigger a real audit event (sign an image)
curl -s -X POST http://localhost:8091/api/v1/repositories/dev/alpine/tags/3.18/sign \
  -H "Authorization: Bearer $JWT" -d '{"signer_id":"smoke"}'

# 5) Check the receiver log — should see two POSTs, both sig-ok=true
```

Verified live on 2026-06-23 against the docker-compose dev stack.
The `image.signed` event from step 4 flowed through audit DB INSERT
→ exporter dispatch → HMAC-signed POST → receiver in ~3 seconds end
to end.

---

## 9. File reference

| File | Why it exists |
|---|---|
| `services/audit/migrations/20260623100000_audit_export_configs.sql` | Schema |
| `services/audit/internal/repository/audit_export.go` | Repo methods + observability counters |
| `services/audit/internal/handler/audit_export.go` | gRPC handler (Get/Put/Delete/Test) + AES-256-GCM seal/open |
| `services/audit/internal/export/export.go` | Renderers + transport + SSRF guard + retry |
| `services/audit/internal/export/tester.go` | Synthetic-event delivery used by the Test RPC |
| `services/audit/internal/eventconsumer/consumer.go` | Dispatcher wired after each INSERT |
| `services/management/internal/handler/workspace_audit_export.go` | BFF HTTP routes |
| `frontend/src/lib/api/audit-export.ts` | TanStack Query hooks |
| `frontend/src/routes/_authenticated.workspace.audit-export.tsx` | Settings page |
| `proto/audit/v1/audit.proto` | RPC definitions |

---

> **Last updated:** see `git log -- docs/SIEM-EXPORT.md`.
> **Adding a new format?** Touch §3, add a renderer in
> `services/audit/internal/export/export.go`, update the format enum
> in both the SQL CHECK constraint and the handler validator.

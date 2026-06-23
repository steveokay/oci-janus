# Security Policy

We take security seriously. OCI-Janus runs in the supply-chain layer of every workload it hosts — a vulnerability here can have wide blast radius. We appreciate every responsible disclosure and aim to respond quickly.

> **Looking for the security audit log** (`SEC-NNN` / `PENTEST-NNN` resolution notes)?
> That lives at [`security.md`](../security.md) in the repo root.
> This file is the **policy** — how to report new vulnerabilities + what to expect.

---

## Supported Versions

The project doesn't have stable releases yet — `main` is the supported branch. Once we tag `v1.0.0`, this section will list which release lines receive security patches.

| Version | Supported |
|---|---|
| `main` (latest) | ✅ |
| Pre-`v1.0.0` releases | ❌ (no released versions yet) |

---

## Reporting a Vulnerability

**Do not open a public GitHub Issue for security vulnerabilities.** Public disclosure before a fix is available risks exploitation against users who haven't yet patched.

Use one of the private channels below.

### Preferred — GitHub Private Vulnerability Reporting

Open a [Private Security Advisory](https://github.com/steveokay/oci-janus/security/advisories/new) on the repository. This routes the report directly to the maintainers without making it visible to anyone else.

### Alternative — Email

If GitHub's private reporting doesn't work for you, contact the maintainer directly via the email address on their GitHub profile. Encrypt with PGP if you have a public key for them.

### What to include

| Field | Why |
|---|---|
| **Affected component** | Which service / library / endpoint. e.g. `services/auth /api/v1/login` |
| **Affected versions** | Commit SHA(s) or "all of main as of `<date>`" |
| **Severity (your assessment)** | CRITICAL / HIGH / MEDIUM / LOW — use CVSS 3.1 or your best judgment |
| **Steps to reproduce** | Minimal reproduction. Curl commands, code snippets, attack flow diagrams |
| **Impact** | What an attacker gains. Auth bypass? RCE? Data exfil? DoS? Privilege escalation across tenants? |
| **Suggested fix** | Optional but appreciated. Patch, mitigation, or design suggestion |
| **Disclosure timeline** | When you'd like to publish. Default is 90 days from acknowledgement |

---

## Our Response Commitment

| Action | Within |
|---|---|
| Acknowledge receipt | 72 hours |
| Initial assessment + severity confirmation | 7 days |
| Status updates while we're working on a fix | Every 7-14 days |
| Fix availability | Depends on severity (see below) |
| Public disclosure + advisory | After fix lands; coordinated with reporter |

Severity-based fix targets (best-effort, not contractual):

| Severity | Target time to fix |
|---|---|
| CRITICAL (RCE, auth bypass, cross-tenant data exfil) | 7 days |
| HIGH (privilege escalation, secret exposure) | 14 days |
| MEDIUM (information disclosure, DoS, weakened crypto) | 30 days |
| LOW (cosmetic, hardening, defence-in-depth) | 90 days |

---

## Disclosure Policy

We follow [coordinated disclosure](https://www.cisa.gov/coordinated-vulnerability-disclosure-process):

1. **You report privately.** We acknowledge within 72 hours.
2. **We investigate + fix.** We keep you updated on progress.
3. **We deploy the fix** (or release a patched version once we have tagged releases).
4. **We publish a public advisory** crediting you (or anonymously if you prefer) once users have had a reasonable window to update.

We will not pursue legal action against researchers who:
- Make a good-faith effort to avoid privacy violations, data destruction, and service degradation during research
- Give us reasonable time to fix issues before public disclosure
- Don't exploit the vulnerability beyond what's necessary to demonstrate it

---

## Hall of Fame

Once we have our first confirmed reports, we'll credit researchers here. Contributions of any kind (a CVE-worthy finding, a thoughtful hardening suggestion, a doc clarification) are valued.

---

## Historical Security Work

The platform underwent three rounds of internal pentest + remediation before going open-source:

- **Round 1** (SEC-001..SEC-036) — initial hardening sweep, all RESOLVED
- **Round 2** (PENTEST-001..PENTEST-026) — post-merge review, all RESOLVED
- **Round 3** (PENTEST-027..PENTEST-033) — third audit, 2 HIGH resolved same day; 1 LOW (`PENTEST-030`) + 1 LOW PARTIAL (`PENTEST-033`) remain open as low-priority follow-ups

Full per-issue resolution notes live in [`security.md`](../security.md) at the repo root. Currently open security items are also surfaced in [`status-tracker.md`](../status-tracker.md) for ongoing attention.

---

## Defence-in-Depth Summary

A few of the design choices that make this platform resilient — useful context for reporters scoping severity:

| Layer | Defence |
|---|---|
| **Transport** | mTLS between every internal service. TLS 1.2+ outbound. Public-facing TLS terminates at the gateway. |
| **Identity** | JWT RS256 with 300s TTL + JTI revocation in Redis. API keys hashed with Argon2id. RBAC scope-checked on every BFF route. |
| **Tenancy** | Every row has `tenant_id NOT NULL`. PostgreSQL Row Level Security as a second layer. Storage keys prefixed with tenant_id. |
| **Storage** | Pluggable backends with at-rest encryption. AES-256-GCM for sensitive columns (OAuth client_secret, audit-export hmac_secret, proxy upstream creds). |
| **Crypto** | Vault Transit for signing keys — exportable=false, the private key never leaves Vault. |
| **Inputs** | Allowlist regex on every user-supplied string before SQL or shell. Parameterised queries via pgx; never `fmt.Sprintf` for SQL. |
| **Outputs** | SSRF guards on every outbound HTTP (proxy, webhooks, SIEM export) — block RFC 1918, loopback, link-local, CGNAT. |
| **Audit** | Immutable audit log with `FORCE ROW LEVEL SECURITY` + low-privilege `registry_audit_app` role. Optional streaming to operator's SIEM. |
| **Supply chain** | Signed-image admission (Cosign), per-repo trusted-key allowlist, tag immutability. |
| **Operations** | `/metrics` on dedicated port `:9090` (SEC-025); NetworkPolicy stencils in Helm chart. |

Full architectural context in [`CLAUDE.md`](../CLAUDE.md) and [`prod-flow.md`](../prod-flow.md).

---

> Thanks for helping us keep OCI-Janus secure. 🙏

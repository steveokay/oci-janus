# Access & identity

This section covers who can do what: organization membership and roles, and the
credentials — API keys and service accounts — that CI systems and automation
use.

## Organizations & members

**Sidebar → Access → Organizations** (`/members`) lists every organization in
the workspace as a card with its repository count. Click a card to manage that
org's members.

### Organization members

`/orgs/{org}/members` shows the org's members in a table:

- Columns: **User, Role** (owner / admin / writer / reader), **Granted by**, and
  an **Actions** column with **Remove**.
- **Add member** opens a dialog to search for a user and assign a role.

!!! note "Admin-gated"
    Only **owner** or **admin** members can add or remove members. An admin
    cannot grant or revoke **owner** (or other admins); only an owner can. RBAC
    is enforced server-side.

### Organization settings

`/orgs/{org}/settings` (linked from the members page) configures **org-wide
defaults** that apply to every repository under the org — a per-repo override
always wins:

- **Default retention** — max age, max count, max storage, dangling grace, and
  max idle.
- **Default scan policy** — which severities block.

Editing requires org admin/owner.

### Tenant users

!!! info "Multi-mode only"
    This surface exists only in multi-tenant deployments.

**Sidebar → Access → Tenant users** (`/tenant/users`) is a tenant-admin console
to **invite** users (they redeem a one-time token to set a password),
**disable/re-enable** accounts, and **elevate** a user to org admin. You cannot
disable your own account.

## API keys & machine identity

**Sidebar → Access → API keys** (`/api-keys`) is a hub with a left rail. The
**Yours** section is visible to everyone; the **Workspace** section is
admin-only.

### Personal keys

Your own long-lived credentials for CI, Terraform, and scripts.

- **Issue key** creates a key; the plaintext secret is shown **exactly once** at
  creation — copy it then. It never appears again.
- The table lists key ID, issued/last-used dates, and an expiry badge (Active /
  Expiring soon / Expired), with **Revoke** per row.

An API key is presented to the registry as a Bearer token of the form
`key.<uuid>.<secret>`; see [Authentication](../AUTH.md) for the wire format.

### Workspace surfaces (admin-only)

The **Workspace** section of the rail exposes:

| Surface | Route | Purpose |
|---|---|---|
| **Service accounts** | `/api-keys/service-accounts` | Machine identities that issue scoped, independently-rotated keys. Create one, then issue/rotate keys from its detail drawer; disable or delete to invalidate all its keys. |
| **Activity** | `/api-keys/activity` | Authenticated requests made by keys and service accounts. Filter by principal (admins) and time range (24h / 7d / 30d / all). |
| **Credential helpers** | `/api-keys/helpers` | Copy-paste CLI credential-helper setup — see [Credential helpers](../CREDENTIAL-HELPERS.md). |
| **Federated trust** | `/api-keys/trust` | OIDC/SPIFFE workload-identity federation (e.g. GitHub Actions) — see [Workload identity](../WORKLOAD-IDENTITY.md). |
| **Token policies** | `/api-keys/policies` | Expiry/scope/MFA rules applied to issued tokens — see [Token policies](../TOKEN-POLICIES.md). |
| **Access review** | `/api-keys/review` | Periodic certification of active credentials for compliance — see [Access reviews](../ACCESS-REVIEW.md). |

!!! note "Admin-gated"
    Everything in the Workspace section requires a workspace-admin role.
    Non-admins simply do not see the section, and direct navigation falls back
    to the personal-keys page.

## Your profile

The **user menu → Profile** (and **Settings → Account**) is where you manage your
own login: change your password, enrol or disable **TOTP MFA** (with one-time
backup codes), and review or revoke your **active sessions**. That screen is
documented under [Settings › Account](settings.md#account).

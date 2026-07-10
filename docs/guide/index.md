# Using the dashboard

The OCI-Janus dashboard is the web console for everyday work: browsing
repositories, reviewing vulnerability scans, managing access, and configuring
integrations. This guide walks through it screen by screen.

If you have not brought the platform up yet, start with the
[Quick start](../getting-started.md).

## Signing in

Open the dashboard in a browser and you land on **`/login`**.

- **Password sign-in** is the primary path. Enter your username (3–64
  characters) and password, then **Sign in**. Failed logins return a
  deliberately vague error and never reveal whether a username exists; the one
  exception is a **temporary account lockout**, which tells you roughly how many
  minutes to wait.
- **Single sign-on.** The login screen shows SSO provider buttons (Google,
  GitHub, Microsoft, SAML). SSO is configured at the **deployment** level rather
  than in the UI — see [SAML SSO](../SAML.md) and [Authentication](../AUTH.md)
  for how providers are wired.
- **Multi-factor (TOTP).** If your account has 2FA enabled, sign-in pauses on a
  **6-digit code** step after your password is accepted. You can switch to
  **"Use backup code instead"** if you have lost your authenticator. If an
  administrator has *forced* MFA enrolment, a non-dismissible dialog walks you
  through scanning a QR code, verifying a code, and saving one-time backup codes
  before you can continue.

!!! tip "Local dev credentials"
    A throwaway dev stack bootstraps with `admin / Admin1234!`. Never use that
    credential outside local development — production bootstrap reads the
    password from stdin.

## The layout

Every authenticated screen shares the same shell:

- A fixed **left sidebar** (a slide-in drawer on narrow screens) for primary
  navigation.
- A **topbar** with global controls on the right.
- The **main content area** in the centre.

### Sidebar navigation

The sidebar groups destinations by what you are trying to do, not by which
service backs them:

| Group | Items |
|---|---|
| **Registry** | Dashboard, Repositories, Helm charts, Pull-through cache |
| **Security** | Security |
| **Governance** | Activity, Audit streaming |
| **Integrations** | Webhooks |
| **Access** | Organizations, Tenant users, API keys |
| **Settings** (pinned at the bottom) | Settings |

Some entries only appear when they are relevant to your deployment or your role
— for example, **Pull-through cache** is hidden unless the proxy is wired, and
**Tenant users** is a multi-tenant surface. The sections below call out those
conditions where they apply.

### Topbar controls

From left to right on the right-hand side of the topbar:

- **✉️ Email activity** — a dropdown of your recent notification-email
  deliveries with **Sent / Pending / Failed** status. "View all" opens the full
  log. (Email delivery is configured under
  [Settings › Notifications](settings.md#notifications).)
- **🔔 Notifications** — recent workspace events with an unread badge. It polls
  every 60 seconds; **Mark all seen** clears the badge, and **View all** opens
  the [Activity feed](operations.md#activity-feed).
- **Theme toggle** — switch between light and dark.
- **User menu** — your profile and **Sign out**. Service-account sessions show a
  bot chip here instead of a full menu.

## Roles and gating

Access is governed by **organization/repository RBAC** plus a global-admin
primitive:

- **owner** — full control of an organization, including granting and revoking
  every role.
- **admin** — manage members (except owners/other admins) and org/repo settings.
- **writer** — push and pull.
- **reader** — pull only.
- **global admin** (`is_global_admin`) — a platform-wide privilege used for
  cross-tenant and infrastructure surfaces.

Throughout this guide, admonitions flag when a screen or action is limited:

!!! note "Admin-gated"
    Actions marked like this require an **admin** or **owner** role (enforced
    server-side — a non-admin attempt returns a 403, surfaced as an error toast).

!!! info "Deployment mode"
    OCI-Janus runs **single-tenant by default**. A few surfaces (Tenant users,
    the Settings › Platform tab) exist only in **multi-tenant** deployments
    (`DEPLOYMENT_MODE=multi`). These are marked **multi-mode only**.

## Where to next

- [Repositories & tags](repositories.md) — browse, push/pull, and inspect images
  and Helm charts.
- [Security](security.md) — vulnerabilities, scans, remediation, signing, and
  compliance reports.
- [Access & identity](access.md) — organizations, roles, API keys, and service
  accounts.
- [Settings](settings.md) — notifications, integrations, scanning, and platform
  configuration.
- [Operations](operations.md) — the activity feed, SIEM streaming, pull-through
  cache, and webhooks.

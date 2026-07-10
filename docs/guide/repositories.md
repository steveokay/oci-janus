# Repositories & tags

Everything you push lives in a **repository** under an **organization**
(`org/repo`). Container images and Helm charts share the same repositories —
they are all OCI artifacts.

## Browsing repositories

**Sidebar → Registry → Repositories** (`/repositories`) lists every repository
in your workspace.

- **Filter** by name or org with the search box (matches across loaded
  repositories), and narrow by visibility with the **All / Public / Private**
  pills.
- **Sort** by Storage or Created by clicking the column header. Sorting applies
  to the repositories already loaded — use **Load more** to pull the rest of the
  catalogue first.
- Each row shows the repository, a **public 🌐 / private 🔒** badge, storage
  size, and creation date. Click a row to open the repository.

### Creating a repository

Click **New repository** to open the create dialog:

- **Organization** — 2–64 chars, lowercase letters, digits, and hyphens.
- **Repository name** — 1–128 chars, lowercase letters, digits, and `.`, `_`,
  `-` separators.
- **Public / Private** toggle (defaults to private).
- **Description** — optional Markdown, up to 8 KiB.

!!! note "Admin-gated"
    Creating a repository requires an admin role on the target org. If you are a
    **global admin** pushing into an org that has no owner yet, the dialog offers
    an inline **"Claim this org"** affordance that grants you org-admin and
    retries.

You do not have to create repositories in the UI — a `docker push` to a new
`org/repo` creates it on the fly, subject to your permissions.

## Repository detail

Opening a repository (`/repositories/{org}/{repo}`) shows a header with the
repo name and a **Delete** action, a **pull-command card** (the exact
`docker pull` / `helm install` / `oci pull` invocation), and an **activity
sparkline**. Below that are five tabs (the active tab is remembered in the URL,
so you can bookmark or share a deep link):

| Tab | What it does |
|---|---|
| **Tags** | The tag list (default). Filterable by artifact type; each tag shows status, digest, size, and creation date. Click a tag for its detail page. |
| **Members** | Per-repository role assignments that override org roles. **Admin-gated** to edit. |
| **Retention** | Tag-retention policy for this repo (keep-latest, TTL, and similar). **Admin-gated** to edit. |
| **Promotions** | A read-only timeline of image promotions into this repo (see [Image promotion](../IMAGE-PROMOTION.md)). |
| **Settings** | Security and quality controls, below. **Admin-gated.** |

### Repository settings

The **Settings** tab groups controls into sections (with a sticky table of
contents on wide screens):

- **Tag immutability** — block re-pushing an existing tag. With this on, pushing
  a tag that already exists is rejected with `MANIFEST_INVALID`.
- **Signature policy** — require a valid signature before a pull is allowed.
- **CVSS pull policy** — gate pulls on scan results above a CVSS threshold.
- **Trusted keys** — the allowlist of signing keys accepted for this repo.
- **Scan policy** — a per-repo block-on-severity policy that overrides the
  tenant default.

## Tag detail

A tag page (`/repositories/{org}/{repo}/tags/{tag}`) is where you inspect a
single image or artifact. The header carries the digest, size, and created time,
plus **Rescan** and **Delete** actions. Six tabs (URL-remembered):

| Tab | What it shows |
|---|---|
| **Security** (default) | Scan state and findings — see below. A quarantine banner appears if the manifest is quarantined. |
| **Push history** | A chronological timeline of pushes/builds for this tag. |
| **Layers** | Image layers — digest, media type, and size, expandable per layer. |
| **Signing** | Cosign / Notary v2 signature verification: signers, timestamps, and trust state. |
| **Referrers** | OCI referrers attached to this digest (attestations, SBOMs, signatures). |
| **Chart** | Helm `Chart.yaml` metadata, dependencies, and a download link. **Only shown for Helm artifacts.** |

### The Security tab

For an unscanned tag the tab offers a **Trigger scan** button (queues a scan;
available to any user). Once a scan completes you see a **severity bar** and a
findings table:

- Columns: **CVE ID, Package, Severity, Fix version, Affected repos/tags, Last
  seen**.
- Expand a row to see every affected `(repo, tag, digest)` triple, each linking
  back to the relevant tag.

**Rescan** re-runs the scanner against the current digest. Scanning is powered
by a pluggable adapter (Trivy, Grype, Clair) — see [Vulnerability
scanning](../SCANNER.md).

## Helm charts

**Sidebar → Registry → Helm charts** (`/helm`) is the same catalogue filtered to
repositories that contain at least one chart. Because a repository can hold both
images and charts, a repo may appear both here and under Repositories — open it
from either and use the Tags tab to switch artifact views.

Push and install charts as OCI artifacts:

```bash
helm push mychart-0.1.0.tgz oci://localhost:8081/library
helm install my-release oci://localhost:8081/library/mychart --version 0.1.0
```

The per-tag **Chart** tab renders `Chart.yaml` and `values.yaml` inline so you
can inspect a chart without pulling it, and offers a one-click `.tgz` download.

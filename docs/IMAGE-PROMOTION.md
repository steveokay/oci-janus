# Image Promotion Workflow

> Canonical reference for FUT-020 image promotion (with the REM-030 UX
> follow-ups). Read this when you're touching the promote flow — backend,
> frontend, or ops — or when you need to understand what "promote a tag"
> actually does on disk.

---

## TL;DR

- **A promotion is an atomic tag copy.** It takes the live manifest digest
  behind a source tag (e.g. `acme/api:v1.2.3` in a `staging` repo) and
  writes it onto a destination `{org}/{repo}:{tag}` (e.g. `acme/api-prod:v1.2.3`).
  Both tags then point at the *same* manifest digest.
- **No bytes move.** No blobs and no manifests are re-pushed. Promotion is
  a pure metadata operation in `registry-metadata`; the destination tag
  references the existing manifest, so storage stays deduplicated.
- **It's all-or-nothing.** The digest read, the destination-tag upsert, and
  the history-row insert run inside one database transaction. A caller
  observes either every write or none.
- **You need `writer` on both ends.** The BFF gate requires the caller to
  hold repo `writer` (or above) on *both* the source and the destination
  repository. A read-only user on `prod/*` cannot promote a stale image in.
- **REM-030 added a destination-org dropdown + a `create_if_missing`
  switch** so operators can promote into a brand-new repo without a
  separate "create repository" round-trip.

---

## 1. What it does

Promotion answers the "cut a release / move dev → staging → prod" question
without a `docker pull && docker push` cycle. The operator picks a source
tag and a destination `{org}/{repo}:{tag}`; the platform copies the source
tag's *current* manifest digest onto the destination tag.

Because the manifest digest is content-addressed, pointing a second tag at
it costs nothing — the blobs it references already exist in storage. This
is why a promotion is instant regardless of image size, and why it can
never produce a partially-uploaded image on the destination.

The source digest is captured at promotion time and frozen into the history
row (`src_digest`). Today `src_digest` always equals `dst_digest` — the two
columns exist so a future re-sign / re-tag workflow can diverge them without
a schema migration, but the promotion path writes the same value to both.

---

## 2. How atomicity is handled

The whole operation lives in `Repository.PromoteTag`
(`services/metadata/internal/repository/promotions.go`), inside a single
`pgx` transaction with a deferred rollback. On any error before `Commit`,
the rollback unwinds every write — the load-bearing invariant that prevents
a "history row but no matching tag write" (or vice-versa) from ever landing
on disk.

The transaction runs these steps in order:

1. **Resolve the source tag → live `manifest_digest`.** Scoped to the
   source `{org}/{repo}` composite. A missing source tag returns `ErrNotFound`.
2. **Resolve the destination repository** (its `id` + `immutable_tags`
   flag). Missing repo returns `ErrNotFound` — *unless* `create_if_missing`
   is set (see §4).
3. **Check the destination tag's current state** — does it already exist,
   at what digest, and is it individually pinned (`immutable=true`)? A
   missing destination tag is the common case, not an error.
4. **Immutability gate** (see §3).
5. **Upsert the destination tag** (`INSERT … ON CONFLICT (repo_id, name)
   DO UPDATE SET manifest_digest = …, updated_at = now()`), pointing it at
   the source digest.
6. **Insert the `promotions` history row** with `RETURNING`, capturing the
   server-side UUID + `promoted_at`.
7. **Commit.**

The gRPC handler (`services/metadata/internal/handler/grpc.go`,
`PromoteTag`) is a thin wrapper: it parses the tenant + actor UUIDs
(surfacing `InvalidArgument` on malformed input) and forwards everything
else to the repository, which owns the transaction, the immutability gate,
and the rollback contract.

> **Re-promotion is not a no-op.** Promoting a digest that already sits on
> the destination tag still writes a fresh `promotions` row (so the audit
> trail records the operator's intent) and bumps the tag's `updated_at`;
> the digest is unchanged. Callers see the same success signal as a
> first-time promotion.

---

## 3. Interaction with tag immutability

Promotion respects the same immutability contract as a normal push. The
gate fires **only** when *all* of these hold:

- the destination tag already exists, **and**
- it currently points at a **different** digest than the source, **and**
- either the destination repository is `immutable_tags=true` **or** the
  destination tag is individually pinned `immutable=true`.

When it fires, `PromoteTag` returns `ErrImmutableTag`, mapped to gRPC
`FailedPrecondition` and surfaced by the BFF as **HTTP 409 Conflict**
("destination tag is immutable"). The per-tag pin takes precedence over
the repo-wide flag; either signal alone is enough to block the move.

Two cases deliberately slip past the gate:

- **A move onto a fresh tag name** — nothing to overwrite, so immutability
  is irrelevant.
- **A same-digest re-promotion** — the destination already points at the
  incoming digest, so it isn't a move. This mirrors the `checkTagImmutable`
  precedent in `services/core` (a same-digest re-push isn't a mutation).

---

## 4. `create_if_missing` — the REM-030 destination-org behaviour

By default, promoting into a destination repository that doesn't exist
returns **404** — a typo in the destination should be caught up front, not
silently create an empty repo.

Setting `create_if_missing: true` in the request body changes step 2 of the
transaction: when the destination repository row is genuinely absent, it is
created **inside the same transaction** with permissive defaults —
`is_public=false`, `immutable_tags=false`, no signature policy, no CVSS
gate, and a 10 GiB storage quota (mirroring `CreateRepository`'s fallback).
The freshly-created repo is `immutable_tags=false`, so the immutability gate
can only ever fire on a *pre-existing* immutable repo.

Two guard rails on the auto-create path:

- **The destination ORG must already exist.** An unknown org still returns
  `ErrNotFound` (→ 404), the same failure code as a bare typo. Orgs are
  never auto-created — RBAC scope assignments hang off them, so minting one
  from a typo would silently create a security-relevant scope.
- **Auto-create does not bypass RBAC.** The BFF still requires `writer` on
  the destination scope before the request reaches the metadata surface.
- **Concurrent creates are handled.** Two promotions racing into the same
  fresh repo trip the unique index; the loser re-selects the winner's row
  and proceeds.

### REM-030 frontend affordance

The promote dialog (`frontend/src/components/repositories/PromoteTagDialog.tsx`)
gained two things over the original FUT-020 dialog:

1. **Destination org is a dropdown**, populated from the orgs across every
   repository the caller can already see (`useRepositories`), plus any
   writer-tier org memberships from `useMe` (covers a brand-new org with no
   repos yet). It falls back to a free-text input only while both queries
   are still unresolved; the source org is always included so "promote
   within the same repo" works immediately.
2. **A "Create destination repository if it doesn't exist" switch** that
   forwards `create_if_missing`. Default off, matching the 404-on-missing
   contract so operators opt in explicitly.

The dialog does **not** pre-check RBAC — it surfaces the BFF's 403 as a
clear toast so the operator learns which side they lack access to.

---

## 5. API / BFF surface

Two REST routes on `registry-management`
(`services/management/internal/handler/promote_tag.go`, registered in
`handler.go`):

| Method + path | Purpose | Role required |
|---|---|---|
| `POST /api/v1/repositories/{org}/{repo}/tags/{tag}/promote` | Promote the source tag (in the URL) onto a destination (in the body). Returns **201** + the persisted promotion JSON. | `writer` on **both** source and destination |
| `GET /api/v1/repositories/{org}/{repo}/promotions` | Recent promotions touching this repo (source **or** destination side). | `reader` |

**Request body** for the POST:

```json
{
  "dst_org":  "acme",
  "dst_repo": "api-prod",
  "dst_tag":  "v1.2.3",
  "note":     "green-lit release for prod",   // optional, ≤ 256 chars
  "create_if_missing": false                    // optional, REM-030
}
```

The handler validates every identifier against the CLAUDE.md §7 allowlists
(org / repo / tag regexes) before any gRPC call. Under the hood the BFF
calls the metadata `PromoteTag` / `ListPromotions` RPCs
(`proto/metadata/v1/metadata.proto`). `ListPromotions` matches rows where
*either* the source or destination side matches the repo, ordered newest
first; the limit is clamped to `[1, 200]` (BFF passes 50, no client
pagination in v1).

### RBAC gating

The both-sides `writer` requirement is the load-bearing security invariant.
Requiring `writer` on the **destination** stops a read-only user on `prod/*`
from pushing a stale image in via a promotion. Requiring `writer` on the
**source** stops the source being used as a laundering channel and keeps
the audit trail meaningful ("this operator had write access here"). The
history endpoint is gated at `reader` only — promotion history is not
sensitive.

### Actor attribution

The actor is the JWT `sub`. For CLI / bot / service-account API keys with no
shadow user id, the actor is empty → persisted as `NULL` in
`promotions.actor_user_id` → rendered as **"automated"** in the history
table and treated as `"system"` in the audit feed.

### Event emission

After a successful promotion the BFF publishes an **`image.promoted`** event
(`events.RoutingImagePromoted`) carrying the full promotion identity. The
`registry-audit` consumer maps it to an `audit_events` row (action
`image.promoted`, resource pinned to the destination side). Publishing
happens *after* the durable write and is best-effort: a broker blip is
logged loudly but does **not** roll back the promotion — audit can be
replayed from the `promotions` table.

---

## 6. Worked example

Promote `acme/api:v1.2.3` from staging into a prod repo, creating the prod
repo if it doesn't exist yet:

```bash
curl -X POST \
  https://api.example.com/api/v1/repositories/acme/api/tags/v1.2.3/promote \
  -H "Authorization: Bearer $JWT" \
  -H "Content-Type: application/json" \
  -d '{
        "dst_org":  "acme",
        "dst_repo": "api-prod",
        "dst_tag":  "v1.2.3",
        "note":     "green-lit release",
        "create_if_missing": true
      }'
```

Response (`201 Created`):

```json
{
  "id": "9f2c…",
  "src_org": "acme", "src_repo": "api", "src_tag": "v1.2.3",
  "src_digest": "sha256:c64c687c…",
  "dst_org": "acme", "dst_repo": "api-prod", "dst_tag": "v1.2.3",
  "dst_digest": "sha256:c64c687c…",
  "actor_user_id": "3a1b…",
  "note": "green-lit release",
  "promoted_at": "2026-07-03T12:00:00Z"
}
```

`acme/api-prod:v1.2.3` now resolves to the same manifest as the source —
`docker pull …/acme/api-prod:v1.2.3` serves the identical image, with no
byte upload having occurred. The promotion shows up on the **Promotions
tab** of both repo detail pages.

From the dashboard: open a tag's detail page → **Promote** button (tag
header) → fill the destination in the dialog → submit. The history renders
under the repo's **Promotions** tab (`PromotionsTab.tsx`).

---

## 7. File reference

| File | Why it exists |
|---|---|
| `services/metadata/internal/repository/promotions.go` | `PromoteTag` (the transaction: resolve → gate → upsert → history) + `ListPromotions` |
| `services/metadata/internal/handler/grpc.go` (`PromoteTag` / `ListPromotions`) | Thin gRPC wrapper — UUID parsing + required-field validation, forwards to the repository |
| `services/metadata/migrations/00018_promotions.sql` | `promotions` table + the tenant-time and dst-lookup indexes |
| `proto/metadata/v1/metadata.proto` | `PromoteTag` / `ListPromotions` RPCs + `Promotion`, `PromoteTagRequest` (incl. `create_if_missing`) messages |
| `services/management/internal/handler/promote_tag.go` | BFF routes — validation, both-sides `writer` gate, error mapping, `image.promoted` publish |
| `libs/rabbitmq/events/events.go` (`RoutingImagePromoted`, `ImagePromotedPayload`) | The `image.promoted` event definition |
| `services/audit/internal/eventconsumer/consumer.go` | Maps `image.promoted` → an `audit_events` row |
| `frontend/src/components/repositories/PromoteTagDialog.tsx` | Promote dialog — REM-030 org dropdown + `create_if_missing` switch |
| `frontend/src/components/repositories/PromotionsTab.tsx` | Repo detail Promotions history table |
| `frontend/src/components/tags/tag-header.tsx` | The **Promote** button that opens the dialog |
| `frontend/src/lib/api/promotions.ts` | TanStack Query hooks for the promote + history routes |

---

## 8. What promotion does NOT do

- **It does not copy blobs or re-verify content.** It trusts the source
  digest as-is; if the source manifest references a blob that GC later
  sweeps, the destination tag inherits that fate (both point at the same
  digest).
- **It does not re-run scans, signing, or admission policy** on the
  destination. A CVSS gate or `require_signature` flag on the destination
  repo is enforced at *pull* time by `registry-core`, not at promotion time.
- **It does not auto-create organizations** — only repositories, and only
  when `create_if_missing` is set (§4).
- **It does not diverge `src_digest` from `dst_digest`.** The columns are
  split for a future re-sign/re-tag workflow, but today they're always
  equal.

---

> **Last updated:** see `git log -- docs/IMAGE-PROMOTION.md`.
> **Cross-references:** service catalogue + endpoint detail in
> [`SERVICES.md`](SERVICES.md); tag immutability + signed-image admission in
> [`SIGNING.md`](SIGNING.md).
> **Found a gap?** The code is the contract — any divergence between this
> file and the implementation is the file's bug.

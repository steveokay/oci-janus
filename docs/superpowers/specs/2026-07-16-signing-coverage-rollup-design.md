# Design — Workspace-wide Image Signing Coverage rollup

> **Status:** Approved design (2026-07-16). Ready for implementation planning.
> **Scope:** Full-stack, Cosign-only. New BFF aggregation endpoint + frontend
> Signing coverage tab. No proto change, no DB migration.
> **Tracking:** Its own `futures.md` item ("Signing coverage rollup"). This is a
> **visibility/observability** feature — deliberately *distinct* from the deferred
> "Signed-image admission, Phase 3" enforcement work (multi-key quorum, key
> rotation/expiry, Cosign keyless/Fulcio), which changes admission decisions. This
> tab changes no admission decision; it only reports posture.

---

## 1. Problem

Per-tag signature verify and the per-repo trusted-key editor already exist on the
repository pages. What is missing is a **workspace-wide rollup**: an operator has no
single place to answer "which repos are actually signing, how well, and where is my
signing posture soft?"

The Security → **Signing** tab already reserves the slot — today it is a dashed
placeholder (`frontend/src/routes/_authenticated.security.signing.tsx`) that
explicitly notes the workspace-wide rollup BFF does not exist yet and lists the four
things it should surface:

- per-repo signed-tag percentage
- recent signers
- trusted-key allowlist health
- which repos have `require_signature` turned on

## 2. Non-goals

- No Notary v2. The signer service is Cosign-only today; a "Notary v2" column is not
  wired (may return as a future placeholder label, but carries no data).
- No admission-decision changes (no quorum, no rotation/expiry, no keyless binding).
- No inline mutation on the rollup. The rollup is read-only; every actionable control
  (toggle `require_signature`, edit trusted keys) is reached by drilling into the
  existing per-repo Settings tab. This avoids duplicating existing UI and keeps the
  security surface narrow.
- No new proto RPC and no DB migration — pure BFF orchestration over existing gRPC.

## 3. Backend — one new BFF endpoint

`GET /api/v1/signing/coverage?window=50` on `services/management`.

Pure orchestration, mirroring the existing `recent-signers`
(`handleListRecentSigners`) route: no new gRPC method, no proto change, no migration.

**Query params**

- `window` (int, default `50`, cap `200`) — the number of most-recent tags per repo
  over which coverage is computed. The window is bounded on purpose so the fan-out
  stays predictable regardless of workspace size.

**Algorithm**

1. `meta.ListRepositories(tenant)` — enumerate all repos, draining pagination.
2. Per repo, with bounded concurrency (reuse the 16-cap worker pool + ~5s per-call
   timeout pattern already used by FE-API-025 parallel verify):
   - `meta.ListTags(repo, limit=window)` → the `window` most-recent tags + their
     manifest digests.
   - **Dedupe tags by manifest digest** — many tags can point at one manifest, so
     `ListSignatures` is called once per *distinct* digest, not once per tag.
   - `signer.ListSignatures(digest, tenant)` per distinct digest → a digest is
     "signed" if it has ≥1 signature. A tag counts as signed iff its digest is signed.
   - `meta.ListRepositoryTrustedKeys(repo, tenant)` → allowlist size + keys.
   - Recent-signer aggregation over the window: dedupe signatures by `key_id`,
     tracking `signer_id`, `last_signed_at`, `tag_count` (same shape as the existing
     recent-signers route).
3. Derive per-repo fields (see response shape).
4. Derive the workspace `summary`.

**`allowlist_health` enum** (the operator-facing posture signal):

| value | meaning |
|---|---|
| `enforced_with_allowlist` | `require_signature` on **and** ≥1 trusted key — strongest posture |
| `enforced_any_signature`  | `require_signature` on but **empty allowlist** → ANY valid signature passes. This is the soft spot operators routinely miss; surfaced as a first-class warning. |
| `advisory`                | `require_signature` off — signing is informational only |

Plus `stale_trusted_keys`: count of approved keys that produced **no** signature
within the window (a hint that an allowlist entry may be dead).

**Graceful degradation**

- `SIGNER_GRPC_ADDR` unset → return `200` with `signer_enabled: false` and signature
  fields omitted (never a `404`/error). Matches how `recent-signers` degrades so the
  tab renders a "signing not wired" state instead of an error toast.
- A per-repo signer/metadata blip drops that repo's signature data to unknown (fail-
  open, warn) rather than failing the whole response — consistent with the fail-open
  posture of the admission path.

**Caching**

- A short (~60s) server-side TTL cache keyed by `(tenant_id, window)` shields the
  backend from the repeated fan-out (many operators loading the same tab). The
  frontend hook additionally sets a 60s react-query `staleTime`.

**Response shape**

```json
{
  "window": 50,
  "signer_enabled": true,
  "summary": {
    "repo_count": 42,
    "repos_require_signature": 12,
    "repos_enforced_empty_allowlist": 3,
    "workspace_signed_tag_pct": 0.68
  },
  "repos": [
    {
      "org": "acme",
      "repo": "api",
      "require_signature": true,
      "window": 50,
      "tags_in_window": 40,
      "signed_tags": 38,
      "signed_pct": 0.95,
      "trusted_key_count": 2,
      "allowlist_health": "enforced_with_allowlist",
      "stale_trusted_keys": 0,
      "recent_signers": [
        { "key_id": "…", "signer_id": "…", "last_signed_at": "…", "tag_count": 12 }
      ]
    }
  ]
}
```

**Authz**: reader-allowed (read-only posture data), same bar as the per-repo
trusted-keys list and recent-signers routes.

**Files touched**

- `services/management/internal/handler/signing_coverage.go` (new handler)
- `services/management/internal/handler/handler.go` (route registration under the
  existing signer-optional block)
- handler test alongside (`signing_coverage_test.go`)

## 4. Frontend — replace the placeholder with the live tab

Stays under Security → **Signing**; no nav or route-tree change. Replace the dashed
placeholder body in `frontend/src/routes/_authenticated.security.signing.tsx`.

**Data hook**: `useSigningCoverage(window)` in
`frontend/src/lib/api/signing-coverage.ts` — TanStack Query, 60s `staleTime`, query
key `signingCoverageKeys.rollup(window)`. Returns the response shape from §3, plus a
`SIGNING_DISABLED` sentinel path for `signer_enabled:false` (mirroring
`useSignaturesByDigest`).

**Layout**

- **Summary strip** — 4 cards, mirroring the scanner-health page vocabulary:
  1. *Repos requiring signature* — `12 / 42`
  2. *Workspace signed-tag coverage* — `68%`
  3. *Enforced w/ empty allowlist* — `3` (warning tone; this is the soft spot)
  4. *Distinct recent signers*
- **Coverage table** — one row per repo, sortable columns, a free-text repo filter,
  and a "requires-signature only" toggle:

  | Repository | Policy | Signed coverage | Trusted keys | Recent signers | |
  |---|---|---|---|---|---|
  | `acme/api` | `require_signature` badge | `CoverageBar` + `38/40` | count + health badge | signer pills | → Settings |

  - `CoverageBar` color-coded: green ≥ 90%, amber 50–89%, red < 50%.
  - Health badge colors map to `allowlist_health`; `enforced_any_signature` renders
    amber with an "any signature" label; `stale_trusted_keys > 0` adds a subtle hint.
  - Rightmost cell drills into the existing per-repo Settings tab (trusted-key editor
    + policy toggle). No controls duplicated on the rollup.
  - Signer pills resolve service-account signer IDs to display names via the existing
    `resolveSignerLabel` helper (FUT-009).
- **States**
  - `signer_enabled: false` → reuse the existing "signing not wired" disabled card.
  - Zero repos → neutral empty state.
  - Window disclosure caption: "Coverage computed over the 50 most-recent tags per
    repo" so the percentage is never misread as all-tags.

**New components** (kept small + focused):
`SigningCoverageSummary`, `SigningCoverageTable`, `CoverageBar` under
`frontend/src/components/security/signing-coverage/`. Reuse existing badge/pill
primitives.

## 5. Testing

- **BFF handler test** (mocked meta + signer clients):
  - aggregation math (signed_pct, summary counters)
  - digest dedupe (two tags → one `ListSignatures` call, both count signed)
  - each `allowlist_health` branch + `stale_trusted_keys`
  - `signer_enabled:false` degrade path returns 200
  - per-repo blip → fail-open (repo present, signature data marked unknown)
- **Frontend vitest**: `useSigningCoverage` hook + `SigningCoverageTable` across
  signed / partial / disabled states; summary card rendering.
- **CI gates before push** (CLAUDE.md §15):
  - frontend: `npm run lint && npm run typecheck && npm run test && npm run build`
  - backend: the `services/management` Makefile target (vet + lint + test + build).

## 6. Docs & tracker hygiene

- `docs/SIGNING.md` — new "Coverage rollup" section (endpoint + `allowlist_health`
  semantics).
- `README` capability matrix — note the workspace signing rollup.
- `futures.md` — add a **new** item "Signing coverage rollup — DONE", kept explicitly
  separate from the deferred "Signed-image admission, Phase 3" enforcement work.
- `status.md` / `status-tracker.md` per the tracker-hygiene rule.

## 7. Risks / open questions

- **Fan-out cost on very large workspaces.** Mitigated by the bounded window + server
  TTL cache. If a deployment outgrows on-demand aggregation, the escalation path is a
  denormalized signed-tag counter in metadata (updated on sign/push events) — out of
  scope here, noted as the future lever.
- **Coverage is windowed, not exhaustive.** Accepted trade-off; the UI discloses the
  window so the number is not misread.

# Generic Artifacts surface for non-image/helm OCI artifacts (FUT-078) — design

> **Date:** 2026-07-12
> **Status:** direction approved → spec for review
> **Builds on:** the unified artifact catalog (`2026-07-11-unified-artifact-catalog-design.md`, shipped PR #321) which folded Helm into the environments → repo → tag structure and explicitly deferred first-class surfacing of non-image/helm derived types.

## Problem

The backend already stores **any** OCI artifact. `deriveArtifactType`
(`services/metadata/internal/repository/repository.go:1045`) maps a manifest's
`config.mediaType` (or, for indexes, its top-level `mediaType`) to one of five
stable discriminators — `image`, `helm`, `signature`, `sbom`, `other` — and
falls through to `other` for any unrecognized-but-present config media type
rather than rejecting the push. `ListRepositories` even implements a working
`?artifact_type=other` filter (the `NOT (config_media_type = ANY(...))` path at
`repository.go:208`).

So the OCI data plane is complete: an ML model (KitOps ModelKit, raw weights via
ORAS), a WASM module, a Flux/Argo GitOps bundle, an OPA policy bundle, an
OpenTofu module, an in-toto/SLSA attestation, or any arbitrary ORAS-pushed file
can be pushed, stored, digest-addressed, tagged, and pulled **today** with no
backend change.

The gap is purely **presentation**. The UI gives first-class browsing to only
two of the five kinds (`image` → `/repositories`, `helm` → the unified catalog's
amber filter/badge). Everything in the `other` bucket is stored but effectively
anonymous: a repo holding only an `other` artifact still appears in the repo list
(the query returns any repo with manifests), but the unified-catalog UI badges
only `image`/`helm` — the approved catalog spec says *"omit non-image/helm from
row badges in v1."* So the operator sees an unlabeled row and has no way to learn
what the artifact actually is.

## Goal

Make every stored artifact **visible and identifiable** in the dashboard without
building a taxonomy per type. Two slices, only the first committed:

- **Slice 1 (committed) — a generic "Artifacts" surface** that lists the `other`
  bucket showing each artifact's **raw `config.mediaType` / `artifactType`
  string**, size, tags, and the manifest-JSON inspector.
- **Slice 2 (documented, deferred) — promote a specific kind to first-class**
  (own badge + optional sidebar entry), first candidate `model`, gated on real
  demand because the cost is design, not code.

Non-goal: reclassifying `signature`/`sbom`. Those are correctly contextual today
(tag Signing / SBOM tabs, referrers panel) because they attach to an image — they
are not standalone browsable artifacts.

## Slice 1 — Generic Artifacts viewer (committed)

**No new taxonomy.** Reuses `deriveArtifactType`'s existing `other` bucket and the
already-implemented `?artifact_type=other` filter. Zero classification changes.

**Backend — surface the raw media type (verify-before-add).**
The one thing the FE lacks is the *raw* type string to display in place of a
generic "other" pill. The proto `Tag`/`Manifest` messages already carry
`config_media_type` (populated in `repository.go` scans; see
`manifestSelectCols` and the `ConfigMediaType` assignments). Before adding
anything, **verify** that `config_media_type` reaches the BFF tag/manifest JSON;
if it does, the backend work is nil. If a manifest also carries a top-level
`artifactType` (OCI 1.1 artifact manifests) that is more specific than
`config.mediaType`, surface that too — otherwise `config.mediaType` is the label.

**BFF.** Ensure the repositories/tags responses expose the raw media-type string
(pass-through; no new gRPC call).

**Frontend.**
- Add an **"Other / Artifacts" lens** to the unified catalog. Leaning toward a
  **fourth filter chip** (All / Images / Charts / **Other**) on the per-env repo
  list rather than a new top-level sidebar route — consistent with the
  environments-first IA that just shipped, and cheaper than a standalone page.
  Final placement (chip vs. sidebar entry) is a plan-time decision.
- Selecting the lens drives the existing `?artifact_type=other` filter.
- Row/tag rendering shows: **raw type string** (verbatim, e.g.
  `application/vnd.kitops.modelkit.config.v1+json`), size, tags, and a link to the
  **manifest-JSON inspector** (reuse FUT-068's inspector — do not build a new one).
- A neutral badge tone (distinct from cyan image / amber helm) for `other` rows.

**Testing.**
- Metadata/BFF: an `other` repo's tag response carries the raw media-type string.
- FE vitest: the Other lens lists `other` repos; the raw type string renders; the
  inspector link resolves.
- No OCI conformance impact — this is a read-only presentation surface.

## Slice 2 — Promote a kind to first-class (documented, deferred)

The **mechanism** is genuinely one row + one switch case, as the code documents
(`artifact_type_test.go:18`, `repository.go:1045-1071` + `configMediaTypesFor`):
add a `case` to `deriveArtifactType`, an entry to `configMediaTypesFor`, a badge,
and an optional filter chip. That part is cheap.

The **cost is design**, and these questions are the actual deliverable of Slice 2
— captured here, not answered, and not built until demand appears:

1. **Scanner-policy applicability.** Should a vulnerability scanner run against ML
   weights / a WASM module / a policy bundle at all? Per-tenant scan policies
   (`services/scanner`) assume an image filesystem. A promoted `model` kind needs
   an explicit "scanning N/A" posture or a model-appropriate scanner, not the
   image default silently applied.
2. **Referrer semantics.** Do `signature` / `sbom` referrers make sense for a
   non-image subject? Cosign/SBOM attach to images; a model may want provenance
   attestations instead. Splitting `attestation` out of `signature` (in-toto/SLSA
   DSSE is semantically distinct from a Cosign signature) is part of this.
3. **Retention / GC semantics.** Do the tag-retention rules and mark-sweep GC
   treat a model tag the same as an image tag? Weights are large and versioned
   differently; the retention defaults may be wrong for them.

**First candidate:** `model` (the biggest 2026 trend). `wasm` and splitting
`attestation` from `signature` follow the same earn-it path — each gets promoted
only when someone is actually pushing that kind, the way `/helm` earned its place.

## Data flow (Slice 1)

```
/repositories/$org?type=other  → GET /api/v1/repositories?org=<org>&artifact_type=other   (filter already exists)
   │  rows carry raw config_media_type → neutral badge + verbatim type string
   ▼
/repositories/$org/$repo       → tag list; each `other` tag links to the manifest inspector (FUT-068)
```

## Out of scope (deferred)

- Any change to `deriveArtifactType`'s five-bucket classification (Slice 2 only).
- First-class `model` / `wasm` / `attestation` kinds — Slice 2, demand-gated.
- Reclassifying `signature` / `sbom` away from their contextual tabs (correct as-is).

## Open decisions folded in (override in review if wrong)

1. Slice 1 surfaces `other` as a **filter chip on the unified catalog**, not a new
   sidebar route. ← the main reversible call; revisit if `other` volume warrants a
   dedicated page.
2. The `other` badge uses a **neutral tone**, distinct from cyan/amber, and shows
   the **raw media-type string** rather than a friendly name (no per-type name map
   in v1).
3. Slice 2 is **documented but not built** — the tracked item commits only to
   Slice 1.

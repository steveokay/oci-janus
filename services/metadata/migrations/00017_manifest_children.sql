-- +goose Up

-- REM-021 — image-index child reachability.
--
-- An OCI image index (application/vnd.oci.image.index.v1+json) and a
-- Docker manifest list (application/vnd.docker.distribution.manifest.list.v2+json)
-- describe a multi-arch image as a parent manifest pointing at N per-
-- platform child manifests. The current data model treats every row in
-- `manifests` as independent — the parent↔child relationship lives only
-- inside the parent's raw JSON.
--
-- Two operator-visible bugs come out of that:
--
--   1. Tag size_bytes for a multi-arch tag was the sum of the child
--      manifest *doc* sizes (~400 B each) instead of the actual layer
--      totals (~123 MB for a single linux/amd64 alpine image).
--   2. Retention's evaluator classifies child manifests in isolation —
--      they have `tags=[]` and look dangling, so `dangling_grace_days`
--      and `max_age_days` mark them for delete even though the tagged
--      parent index still references them. From the dashboard the
--      operator sees "retention is picking individual arches inside my
--      tag" which is not the design.
--
-- This table persists the parent→child link at PutManifest time when
-- the new manifest is an index / manifest list. Downstream:
--
--   * services/metadata.loadManifestsForEval joins through this table
--     so a child's effective tag set is `direct_tags ∪ parent_tags`.
--   * services/metadata.parseImageSize sums child `image_size_bytes`
--     when the parent is an index, in place of the buggy
--     manifests[].size sum.
--   * Single-arch images leave this table empty and behave exactly as
--     before — zero migration risk on existing data.
--
-- repo_id + tenant_id are denormalised onto every row so the JOIN to
-- manifests stays one hop. The composite PRIMARY KEY (tenant_id,
-- parent_digest, child_digest) gives us idempotent upsert + tenant
-- isolation in one constraint. ON DELETE CASCADE from repositories
-- mirrors the existing manifests/tags chain so a repo delete cleans up
-- the child mapping without an extra app-side sweep.

CREATE TABLE manifest_children (
    repo_id        UUID NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    tenant_id      UUID NOT NULL,
    parent_digest  TEXT NOT NULL,
    child_digest   TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, parent_digest, child_digest)
);

-- Reachability lookup pattern (from retention.eval): "for this child
-- digest, what tagged parents reference it?". The leading column is
-- child_digest so the index serves the eval JOIN without scanning the
-- whole partition. tenant_id is included so the read stays
-- tenant-scoped from the first column comparison.
CREATE INDEX manifest_children_child_idx
    ON manifest_children (tenant_id, child_digest);

-- Parent-side reverse lookup pattern (from parseImageSize when we want
-- to sum children for a freshly-pushed parent): "for this parent
-- digest, list child digests". The PRIMARY KEY already serves this —
-- (tenant_id, parent_digest, *) is a usable prefix scan — so no
-- additional index is required.

-- repo_id is denormalised but not part of the PK because a manifest
-- digest is content-addressable: the same digest cannot collide across
-- repos within a tenant in any realistic shape, and treating
-- (tenant_id, parent_digest, child_digest) as the natural key keeps
-- the upsert path single-row + idempotent for the common multi-tag
-- re-push case.

-- +goose Down

DROP TABLE IF EXISTS manifest_children;

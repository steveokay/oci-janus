-- +goose Up

-- Tag immutability (futures.md Tier 1 #2).
--
-- Two complementary flags:
--
--   repositories.immutable_tags  — repo-wide "no tag re-pushes" gate.
--                                  When TRUE, services/core's PutManifest
--                                  rejects a re-push to ANY existing tag
--                                  with a different manifest_digest.
--   tags.immutable               — per-tag pin. When TRUE, that single tag
--                                  cannot be moved to a new digest even
--                                  on a non-immutable repo.
--
-- Both default FALSE so the migration is transparent to existing tenants
-- — operators opt in either repo-wide (the recommended posture for
-- production-promotion repos like `staging` + `prod`) or per-tag (useful
-- when a single canonical tag like `latest-release` needs locking down
-- without restricting the whole repo).
--
-- Why both rather than just one:
--   * Repo-wide is the table-stakes posture an enterprise customer
--     refuses to deploy without — a single "this repo's tags don't move"
--     toggle.
--   * Per-tag is the lighter alternative for repos that mix mutable
--     dev tags + a small set of pinned release tags. Without it the
--     operator would have to fork the repo to gain the pin granularity.
--
-- The rejection path enforces "EITHER flag plus a digest mismatch =
-- reject" — see services/core/internal/service/registry.go PutManifest
-- for the precedence rule.

ALTER TABLE repositories
    -- Default FALSE preserves the pre-migration "tags are mutable"
    -- behaviour. Operators flip this via PATCH /repositories/{org}/{repo}
    -- once they've decided their immutability posture.
    ADD COLUMN immutable_tags BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE tags
    -- Default FALSE preserves pre-migration mutability. Pinning is
    -- per-tag — the partial index below keeps lookups cheap even when
    -- the repo holds thousands of mutable tags + a handful of pins.
    ADD COLUMN immutable BOOLEAN NOT NULL DEFAULT FALSE;

-- Partial index supports the future "list all pinned tags in this repo"
-- admin surface + the rejection-path EXISTS check in services/core.
-- Tiny because the predicate excludes the common case.
CREATE INDEX idx_tags_immutable
    ON tags(repo_id)
    WHERE immutable = TRUE;

-- +goose Down

DROP INDEX IF EXISTS idx_tags_immutable;
ALTER TABLE tags             DROP COLUMN IF EXISTS immutable;
ALTER TABLE repositories     DROP COLUMN IF EXISTS immutable_tags;

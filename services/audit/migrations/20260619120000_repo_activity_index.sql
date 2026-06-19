-- +goose Up
-- +goose StatementBegin

-- FE-API-004: index on (tenant_id, repository_name, occurred_at) so the
-- GetRepoActivity query (filter by tenant + repository_name extracted from
-- the JSON payload, sort newest-first) doesn't have to fall back to the
-- tenant-only index and post-filter every row.
--
-- The expression `(metadata->'raw'->>'repository_name')` matches the JSON
-- path used by repository.GetRepoActivity. We intentionally index a partial
-- key (only rows where the field is present) to keep the index small —
-- tenant.created and similar non-repo-scoped events never need to appear here.
CREATE INDEX idx_audit_events_repo_activity
    ON audit_events (
        tenant_id,
        (metadata->'raw'->>'repository_name'),
        occurred_at DESC,
        id DESC
    )
    WHERE metadata->'raw'->>'repository_name' IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_audit_events_repo_activity;

-- +goose StatementEnd

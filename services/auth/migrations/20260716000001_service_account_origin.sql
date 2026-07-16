-- +goose Up
-- +goose StatementBegin

-- MCP one-click-connect provenance. 'manual' (default) marks operator-created
-- service accounts; 'mcp-connect' marks SAs minted by the MCP connect card
-- (named mcp-agent-<base36>) so they are discoverable/filterable in the FE.
ALTER TABLE service_accounts
    ADD COLUMN origin TEXT NOT NULL DEFAULT 'manual';

-- Backfill existing MCP one-click-connect rows by their name convention so the
-- current mcp-agent-* service accounts become discoverable retroactively.
-- Idempotent: only flips rows still marked 'manual'.
UPDATE service_accounts
    SET origin = 'mcp-connect'
    WHERE name LIKE 'mcp-agent-%' AND origin = 'manual';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE service_accounts DROP COLUMN origin;

-- +goose StatementEnd

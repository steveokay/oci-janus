-- +goose Up

-- REM-011 Phase 2 — persisted selection of the active scanner adapter.
--
-- The adapter registry (services/scanner/internal/registry) discovers
-- every executable scanner-* binary at startup. One of them is "active"
-- at a time; the worker pool dispatches all jobs to that one until an
-- admin swaps it via SetActiveAdapter.
--
-- This table records which binary is currently active so the choice
-- survives a container restart. Without it, every restart would fall
-- back to the SCANNER_PLUGIN_PATH env var (or the discovery default),
-- silently undoing the admin's last swap.
--
-- A single-row "settings" table uses the CHECK-on-singleton trick: the
-- PK is a boolean fixed to TRUE so any attempt to insert a second row
-- collides on the primary key. Simpler than a serial/sequence sentinel
-- and lets the upsert use ON CONFLICT (singleton).
CREATE TABLE scanner_settings (
    singleton           BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    -- active_adapter_path is the absolute on-disk path of the binary
    -- the worker pool should dispatch to. Validated by the registry on
    -- read (an unknown path falls back to the env-var default and is
    -- logged as a degradation).
    active_adapter_path TEXT NOT NULL,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- updated_by is the actor's user_id when set via the BFF, or the
    -- literal string 'system' when set by a startup default. Plain TEXT
    -- (not UUID) so the system sentinel doesn't need a fake UUID.
    updated_by          TEXT NOT NULL DEFAULT 'system'
);

-- +goose Down

DROP TABLE IF EXISTS scanner_settings;

-- +goose Up
-- +goose StatementBegin

-- FUT-004 — access review snooze. Operator-picked deferral timestamp; the
-- weekly access-review worker skips keys whose review_snoozed_until is in
-- the future. NULL = never snoozed (default). The FE surfaces "Snooze 30d"
-- as the primary snooze button but the column accepts any absolute
-- timestamp so future automation can extend the window without a schema
-- change.
ALTER TABLE api_keys ADD COLUMN review_snoozed_until TIMESTAMPTZ;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE api_keys DROP COLUMN IF EXISTS review_snoozed_until;

-- +goose StatementEnd

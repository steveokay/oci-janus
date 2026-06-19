-- +goose Up
ALTER TABLE repositories ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE repositories DROP COLUMN IF EXISTS description;

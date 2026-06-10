-- +goose Up
-- +goose StatementBegin
INSERT INTO tenants (id, name)
VALUES ('00000000-0000-0000-0000-000000000001', 'dev')
ON CONFLICT (id) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM tenants WHERE id = '00000000-0000-0000-0000-000000000001';
-- +goose StatementEnd

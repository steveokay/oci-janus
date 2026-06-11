-- +goose Up
-- +goose StatementBegin
INSERT INTO tenants (id, name)
VALUES ('98dbe36b-ef28-4903-b25c-bff1b2921c9e', 'dev')
ON CONFLICT (id) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM tenants WHERE id = '98dbe36b-ef28-4903-b25c-bff1b2921c9e';
-- +goose StatementEnd

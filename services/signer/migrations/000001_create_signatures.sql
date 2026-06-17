-- +goose Up
-- +goose StatementBegin
CREATE TABLE signatures (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    manifest_digest  TEXT        NOT NULL,
    repository_name  TEXT        NOT NULL,
    signer_id        TEXT        NOT NULL,
    key_id           TEXT        NOT NULL,
    -- signature_digest stores only the sha256:<hex> of the raw sig bytes.
    -- The raw base64 DER bytes are intentionally NOT persisted (SEC-015).
    signature_digest TEXT        NOT NULL,
    signed_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(manifest_digest, signer_id)
);

CREATE INDEX idx_signatures_manifest ON signatures(manifest_digest);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS signatures;
-- +goose StatementEnd

-- +goose Up

-- Track whether 24h and 48h admin notifications have been sent for this domain.
-- next_poll_after implements exponential backoff: the worker only picks up a domain
-- when the current time is past this timestamp, avoiding thundering-herd DNS polls.
ALTER TABLE tenant_domains
    ADD COLUMN notified_24h    BOOLEAN     NOT NULL DEFAULT false,
    ADD COLUMN notified_48h    BOOLEAN     NOT NULL DEFAULT false,
    ADD COLUMN next_poll_after TIMESTAMPTZ NOT NULL DEFAULT now();

-- Replace the partial index so the worker query stays efficient with the new filter.
DROP INDEX IF EXISTS idx_tenant_domains_unverified;
CREATE INDEX idx_tenant_domains_unverified
    ON tenant_domains(next_poll_after, registered_at)
    WHERE verified = false;

-- +goose Down

ALTER TABLE tenant_domains
    DROP COLUMN IF EXISTS notified_24h,
    DROP COLUMN IF EXISTS notified_48h,
    DROP COLUMN IF EXISTS next_poll_after;

DROP INDEX IF EXISTS idx_tenant_domains_unverified;
CREATE INDEX idx_tenant_domains_unverified ON tenant_domains(verified, registered_at)
    WHERE verified = false;

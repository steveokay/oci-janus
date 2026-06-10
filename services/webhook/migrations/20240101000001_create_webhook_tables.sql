-- +goose Up

CREATE TABLE webhook_endpoints (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL,
    url         TEXT NOT NULL,
    events      TEXT[] NOT NULL,
    secret_enc  TEXT NOT NULL,   -- AES-256-GCM encrypted HMAC key (hex)
    active      BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_webhook_endpoints_tenant ON webhook_endpoints(tenant_id);

-- retry_delays[n] is the interval before the (n+1)th delivery attempt.
-- Indexes: 0→5s, 1→30s, 2→5m, 3→30m, 4→2h (matches CLAUDE.md §4.9)
CREATE TABLE webhook_deliveries (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    endpoint_id     UUID NOT NULL REFERENCES webhook_endpoints(id) ON DELETE CASCADE,
    tenant_id       UUID NOT NULL,
    event_type      TEXT NOT NULL,
    payload         JSONB NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending','delivered','failed','dead')),
    attempts        INT NOT NULL DEFAULT 0,
    max_attempts    INT NOT NULL DEFAULT 5,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at    TIMESTAMPTZ
);

CREATE INDEX idx_webhook_deliveries_pending ON webhook_deliveries(next_attempt_at)
    WHERE status = 'pending';
CREATE INDEX idx_webhook_deliveries_tenant ON webhook_deliveries(tenant_id);

-- +goose Down

DROP TABLE IF EXISTS webhook_deliveries;
DROP TABLE IF EXISTS webhook_endpoints;

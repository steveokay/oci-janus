-- +goose Up
-- FUT-019 Phase 3 follow-up — grant the low-privilege runtime role access to
-- the email channel tables.
--
-- The email_transport_config + email_deliveries tables (migration
-- 20260707120000) were created without GRANTs, so the audit service's runtime
-- pool — which authenticates as registry_audit_app (SEC-001 low-privilege role,
-- created in 20240101000002) — could not read or write them. Every
-- GetEmailTransportConfig / ListEmailDeliveries / enqueue / send-loop query
-- failed with "permission denied for table ..." surfaced as codes.Internal.
--
-- Mirrors the grant added for the notification tables in 20260626000001:
-- SELECT + INSERT + UPDATE covers every verb the repository issues (config
-- upsert + test-result update; delivery enqueue, claim-lease, mark-sent,
-- mark-failed, list). No DELETE path exists, so DELETE is deliberately omitted.
GRANT INSERT, SELECT, UPDATE ON email_transport_config TO registry_audit_app;
GRANT INSERT, SELECT, UPDATE ON email_deliveries       TO registry_audit_app;

-- +goose Down
REVOKE INSERT, SELECT, UPDATE ON email_deliveries       FROM registry_audit_app;
REVOKE INSERT, SELECT, UPDATE ON email_transport_config FROM registry_audit_app;

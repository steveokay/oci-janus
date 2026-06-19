// Package repository handles all database access for the webhook service.
// All SQL is parameterised — no dynamic string building.
package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EndpointRecord is a row from webhook_endpoints.
type EndpointRecord struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	URL       string
	Events    []string
	SecretEnc string // encrypted HMAC key
	Active    bool
	CreatedAt time.Time
}

// DeliveryRecord is a row from webhook_deliveries.
type DeliveryRecord struct {
	ID            uuid.UUID
	EndpointID    uuid.UUID
	TenantID      uuid.UUID
	EventType     string
	Payload       []byte
	Status        string
	Attempts      int
	MaxAttempts   int
	NextAttemptAt time.Time
	LastError     string
	CreatedAt     time.Time
	// DeliveredAt is non-zero only when status='delivered'.
	DeliveredAt time.Time
}

// Repository wraps the connection pool and owns all DB queries.
type Repository struct {
	pool *pgxpool.Pool
}

// New returns a Repository backed by the given pool.
func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// CreateEndpoint inserts a new webhook endpoint. Returns the created record.
func (r *Repository) CreateEndpoint(ctx context.Context, tenantID uuid.UUID, url string, events []string, secretEnc string) (*EndpointRecord, error) {
	var rec EndpointRecord
	err := r.pool.QueryRow(ctx,
		`INSERT INTO webhook_endpoints (tenant_id, url, events, secret_enc)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, tenant_id, url, events, secret_enc, active, created_at`,
		tenantID, url, events, secretEnc,
	).Scan(&rec.ID, &rec.TenantID, &rec.URL, &rec.Events, &rec.SecretEnc, &rec.Active, &rec.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("CreateEndpoint: %w", err)
	}
	return &rec, nil
}

// DeleteEndpoint removes a webhook endpoint by id + tenant (prevents cross-tenant delete).
func (r *Repository) DeleteEndpoint(ctx context.Context, endpointID, tenantID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM webhook_endpoints WHERE id = $1 AND tenant_id = $2`,
		endpointID, tenantID,
	)
	if err != nil {
		return fmt.Errorf("DeleteEndpoint: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// ListEndpoints returns all active endpoints for a tenant.
func (r *Repository) ListEndpoints(ctx context.Context, tenantID uuid.UUID) ([]*EndpointRecord, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, tenant_id, url, events, secret_enc, active, created_at
		 FROM webhook_endpoints
		 WHERE tenant_id = $1
		 ORDER BY created_at`,
		tenantID,
	)
	if err != nil {
		return nil, fmt.Errorf("ListEndpoints: %w", err)
	}
	defer rows.Close()

	var out []*EndpointRecord
	for rows.Next() {
		var rec EndpointRecord
		if err := rows.Scan(&rec.ID, &rec.TenantID, &rec.URL, &rec.Events, &rec.SecretEnc, &rec.Active, &rec.CreatedAt); err != nil {
			return nil, fmt.Errorf("ListEndpoints scan: %w", err)
		}
		out = append(out, &rec)
	}
	return out, rows.Err()
}

// FindEndpointsForEvent returns active endpoints that subscribed to the given event type.
func (r *Repository) FindEndpointsForEvent(ctx context.Context, tenantID uuid.UUID, eventType string) ([]*EndpointRecord, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, tenant_id, url, events, secret_enc, active, created_at
		 FROM webhook_endpoints
		 WHERE tenant_id = $1
		   AND active = true
		   AND (events @> ARRAY[$2]::TEXT[] OR events @> ARRAY['*']::TEXT[])`,
		tenantID, eventType,
	)
	if err != nil {
		return nil, fmt.Errorf("FindEndpointsForEvent: %w", err)
	}
	defer rows.Close()

	var out []*EndpointRecord
	for rows.Next() {
		var rec EndpointRecord
		if err := rows.Scan(&rec.ID, &rec.TenantID, &rec.URL, &rec.Events, &rec.SecretEnc, &rec.Active, &rec.CreatedAt); err != nil {
			return nil, fmt.Errorf("FindEndpointsForEvent scan: %w", err)
		}
		out = append(out, &rec)
	}
	return out, rows.Err()
}

// CreateDelivery creates a delivery record for a single endpoint + event.
func (r *Repository) CreateDelivery(ctx context.Context, endpointID, tenantID uuid.UUID, eventType string, payload []byte) (*DeliveryRecord, error) {
	var rec DeliveryRecord
	err := r.pool.QueryRow(ctx,
		`INSERT INTO webhook_deliveries (endpoint_id, tenant_id, event_type, payload)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, endpoint_id, tenant_id, event_type, payload, status,
		           attempts, max_attempts, next_attempt_at, COALESCE(last_error,''), created_at`,
		endpointID, tenantID, eventType, payload,
	).Scan(&rec.ID, &rec.EndpointID, &rec.TenantID, &rec.EventType, &rec.Payload,
		&rec.Status, &rec.Attempts, &rec.MaxAttempts, &rec.NextAttemptAt, &rec.LastError, &rec.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("CreateDelivery: %w", err)
	}
	return &rec, nil
}

// PollDueDeliveries returns up to limit pending deliveries whose next_attempt_at <= now().
func (r *Repository) PollDueDeliveries(ctx context.Context, limit int) ([]*DeliveryRecord, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT d.id, d.endpoint_id, d.tenant_id, d.event_type, d.payload, d.status,
		        d.attempts, d.max_attempts, d.next_attempt_at, COALESCE(d.last_error,''), d.created_at
		 FROM webhook_deliveries d
		 WHERE d.status = 'pending'
		   AND d.next_attempt_at <= now()
		 ORDER BY d.next_attempt_at
		 LIMIT $1
		 FOR UPDATE SKIP LOCKED`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("PollDueDeliveries: %w", err)
	}
	defer rows.Close()

	var out []*DeliveryRecord
	for rows.Next() {
		var rec DeliveryRecord
		if err := rows.Scan(&rec.ID, &rec.EndpointID, &rec.TenantID, &rec.EventType, &rec.Payload,
			&rec.Status, &rec.Attempts, &rec.MaxAttempts, &rec.NextAttemptAt, &rec.LastError, &rec.CreatedAt); err != nil {
			return nil, fmt.Errorf("PollDueDeliveries scan: %w", err)
		}
		out = append(out, &rec)
	}
	return out, rows.Err()
}

// MarkDelivered marks a delivery as successfully delivered.
func (r *Repository) MarkDelivered(ctx context.Context, deliveryID uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE webhook_deliveries
		 SET status = 'delivered', delivered_at = now()
		 WHERE id = $1`,
		deliveryID,
	)
	return err
}

// MarkFailed increments the attempt counter and schedules the next retry.
// If attempts >= max_attempts, the delivery is marked dead.
func (r *Repository) MarkFailed(ctx context.Context, deliveryID uuid.UUID, lastError string, nextAttemptAt time.Time, dead bool) error {
	newStatus := "pending"
	if dead {
		newStatus = "dead"
	}
	_, err := r.pool.Exec(ctx,
		`UPDATE webhook_deliveries
		 SET attempts = attempts + 1,
		     last_error = $2,
		     next_attempt_at = $3,
		     status = $4
		 WHERE id = $1`,
		deliveryID, lastError, nextAttemptAt, newStatus,
	)
	return err
}

// GetEndpoint returns a single endpoint record. Used by the delivery worker.
func (r *Repository) GetEndpoint(ctx context.Context, endpointID uuid.UUID) (*EndpointRecord, error) {
	var rec EndpointRecord
	err := r.pool.QueryRow(ctx,
		`SELECT id, tenant_id, url, events, secret_enc, active, created_at
		 FROM webhook_endpoints WHERE id = $1`,
		endpointID,
	).Scan(&rec.ID, &rec.TenantID, &rec.URL, &rec.Events, &rec.SecretEnc, &rec.Active, &rec.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("GetEndpoint: %w", err)
	}
	return &rec, nil
}

// GetEndpointForTenant is like GetEndpoint but enforces tenant isolation —
// returns pgx.ErrNoRows when the endpoint exists but belongs to a different
// tenant, so a caller in tenant A cannot probe whether an id in tenant B
// exists by status code.
func (r *Repository) GetEndpointForTenant(ctx context.Context, endpointID, tenantID uuid.UUID) (*EndpointRecord, error) {
	var rec EndpointRecord
	err := r.pool.QueryRow(ctx,
		`SELECT id, tenant_id, url, events, secret_enc, active, created_at
		 FROM webhook_endpoints WHERE id = $1 AND tenant_id = $2`,
		endpointID, tenantID,
	).Scan(&rec.ID, &rec.TenantID, &rec.URL, &rec.Events, &rec.SecretEnc, &rec.Active, &rec.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("GetEndpointForTenant: %w", err)
	}
	return &rec, nil
}

// UpdateEndpoint patches a subset of an endpoint's fields. Pass nil for any
// field the caller wants left alone; an empty `events` slice means "leave
// events unchanged" since a zero-event endpoint would be silently broken.
func (r *Repository) UpdateEndpoint(ctx context.Context, endpointID, tenantID uuid.UUID, url *string, events []string, active *bool) (*EndpointRecord, error) {
	var rec EndpointRecord
	err := r.pool.QueryRow(ctx,
		`UPDATE webhook_endpoints
		 SET url    = COALESCE($3, url),
		     events = CASE WHEN cardinality($4::TEXT[]) > 0 THEN $4 ELSE events END,
		     active = COALESCE($5, active)
		 WHERE id = $1 AND tenant_id = $2
		 RETURNING id, tenant_id, url, events, secret_enc, active, created_at`,
		endpointID, tenantID, url, events, active,
	).Scan(&rec.ID, &rec.TenantID, &rec.URL, &rec.Events, &rec.SecretEnc, &rec.Active, &rec.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("UpdateEndpoint: %w", err)
	}
	return &rec, nil
}

// RotateSecret replaces the stored encrypted HMAC secret on an existing
// endpoint. The caller is responsible for encrypting the plaintext before
// passing it here (same pattern as CreateEndpoint).
func (r *Repository) RotateSecret(ctx context.Context, endpointID, tenantID uuid.UUID, secretEnc string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE webhook_endpoints SET secret_enc = $3
		 WHERE id = $1 AND tenant_id = $2`,
		endpointID, tenantID, secretEnc,
	)
	if err != nil {
		return fmt.Errorf("RotateSecret: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// ListDeliveries returns recent dispatch attempts for one endpoint, newest
// first. `since` filters to created_at >= since when non-zero; `limit` caps
// the response (defaults applied by the caller).
func (r *Repository) ListDeliveries(ctx context.Context, endpointID, tenantID uuid.UUID, since time.Time, limit int) ([]*DeliveryRecord, error) {
	// Endpoint ownership is enforced via the tenant_id filter on the JOIN
	// against webhook_endpoints so a tenant cannot see another tenant's
	// deliveries even by guessing the endpoint UUID.
	rows, err := r.pool.Query(ctx,
		`SELECT d.id, d.endpoint_id, d.tenant_id, d.event_type, d.payload, d.status,
		        d.attempts, d.max_attempts, d.next_attempt_at, COALESCE(d.last_error,''),
		        d.created_at, COALESCE(d.delivered_at, 'epoch'::timestamptz)
		 FROM webhook_deliveries d
		 WHERE d.endpoint_id = $1
		   AND d.tenant_id   = $2
		   AND ($3::timestamptz IS NULL OR d.created_at >= $3)
		 ORDER BY d.created_at DESC
		 LIMIT $4`,
		endpointID, tenantID, nilTime(since), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("ListDeliveries: %w", err)
	}
	defer rows.Close()

	var out []*DeliveryRecord
	for rows.Next() {
		var rec DeliveryRecord
		if err := rows.Scan(&rec.ID, &rec.EndpointID, &rec.TenantID, &rec.EventType, &rec.Payload,
			&rec.Status, &rec.Attempts, &rec.MaxAttempts, &rec.NextAttemptAt, &rec.LastError,
			&rec.CreatedAt, &rec.DeliveredAt); err != nil {
			return nil, fmt.Errorf("ListDeliveries scan: %w", err)
		}
		out = append(out, &rec)
	}
	return out, rows.Err()
}

// nilTime returns nil when t is the zero value so the caller can express
// "no since filter" in SQL via IS NULL rather than a synthetic epoch.
func nilTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

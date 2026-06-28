// Package worker contains the RabbitMQ event consumer and the delivery retry loop.
package worker

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/libs/crypto/aes"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/webhook/internal/delivery"
	"github.com/steveokay/oci-janus/services/webhook/internal/repository"
)

// workerRepo is the database interface used by the Worker.
// Extracted so unit tests can substitute a fake without a real PostgreSQL connection.
type workerRepo interface {
	FindEndpointsForEvent(ctx context.Context, tenantID uuid.UUID, eventType string) ([]*repository.EndpointRecord, error)
	CreateDelivery(ctx context.Context, endpointID, tenantID uuid.UUID, eventType string, payload []byte) (*repository.DeliveryRecord, error)
	PollDueDeliveries(ctx context.Context, limit int) ([]*repository.DeliveryRecord, error)
	GetEndpoint(ctx context.Context, endpointID uuid.UUID) (*repository.EndpointRecord, error)
	MarkDelivered(ctx context.Context, deliveryID uuid.UUID) error
	MarkFailed(ctx context.Context, deliveryID uuid.UUID, lastError string, nextAttemptAt time.Time, dead bool) error
}

// workerDispatcher is the HTTP delivery interface used by the Worker.
// Extracted so unit tests can substitute a fake without real network calls.
type workerDispatcher interface {
	Deliver(ctx context.Context, targetURL string, payload []byte, hmacKey []byte) error
}

// Worker drives RabbitMQ event ingestion and the HTTP delivery retry loop.
type Worker struct {
	repo          workerRepo
	dispatcher    workerDispatcher
	credentialKey []byte
	pollInterval  time.Duration
}

// New creates a Worker. credentialKeyHex is the hex-encoded 32-byte AES key used
// to decrypt per-endpoint HMAC secrets stored in the database.
func New(repo *repository.Repository, dispatcher *delivery.Dispatcher, credentialKeyHex string, pollIntervalSecs int) (*Worker, error) {
	return newWithDeps(repo, dispatcher, credentialKeyHex, pollIntervalSecs)
}

// newWithDeps is the internal constructor used by both New and tests.
func newWithDeps(repo workerRepo, dispatcher workerDispatcher, credentialKeyHex string, pollIntervalSecs int) (*Worker, error) {
	key, err := hex.DecodeString(credentialKeyHex)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("CREDENTIAL_KEY_HEX must be a 64-character hex string (32 bytes)")
	}
	return &Worker{
		repo:          repo,
		dispatcher:    dispatcher,
		credentialKey: key,
		pollInterval:  time.Duration(pollIntervalSecs) * time.Second,
	}, nil
}

// HandleEvent is the consumer.Handler for all subscribed event types.
// It looks up matching endpoints and creates delivery records for each.
//
// Defence-in-depth: events with an empty tenant_id are ACKed and dropped
// rather than NACK'd. The webhook subscription model is per-tenant — an
// event without a tenant has nowhere to route. Returning an error here
// would cause RabbitMQ to redeliver until the retry cap is hit. The
// publisher should not emit such events (gc/retention.go fixed at source
// 2026-06-25), but a defensive consumer keeps a future regression from
// stalling the queue.
func (w *Worker) HandleEvent(ctx context.Context, event events.Event) error {
	if event.TenantID == "" {
		slog.WarnContext(ctx, "webhook: dropping event with empty tenant_id (ACK)",
			"event_id", event.ID, "event_type", event.Type)
		return nil
	}
	tenantID, err := uuid.Parse(event.TenantID)
	if err != nil {
		return fmt.Errorf("invalid tenant_id in event %s: %w", event.ID, err)
	}

	endpoints, err := w.repo.FindEndpointsForEvent(ctx, tenantID, event.Type)
	if err != nil {
		return fmt.Errorf("FindEndpointsForEvent: %w", err)
	}

	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	for _, ep := range endpoints {
		if _, err := w.repo.CreateDelivery(ctx, ep.ID, tenantID, event.Type, body); err != nil {
			slog.ErrorContext(ctx, "create delivery record failed",
				"endpoint_id", ep.ID,
				"event_id", event.ID,
				"error", err,
			)
			// Continue — don't fail the whole handler if one endpoint DB write fails.
		}
	}
	return nil
}

// RunDeliveryLoop polls for due deliveries and attempts HTTP delivery.
// It blocks until ctx is cancelled.
func (w *Worker) RunDeliveryLoop(ctx context.Context) {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.processDue(ctx)
		}
	}
}

// processDue fetches up to 50 due deliveries and dispatches them concurrently.
func (w *Worker) processDue(ctx context.Context) {
	deliveries, err := w.repo.PollDueDeliveries(ctx, 50)
	if err != nil {
		slog.ErrorContext(ctx, "PollDueDeliveries error", "error", err)
		return
	}
	for _, d := range deliveries {
		go w.attemptDelivery(ctx, d)
	}
}

// attemptDelivery fetches the endpoint, decrypts the HMAC key, and sends one HTTP POST.
func (w *Worker) attemptDelivery(ctx context.Context, d *repository.DeliveryRecord) {
	ep, err := w.repo.GetEndpoint(ctx, d.EndpointID)
	if err != nil {
		slog.ErrorContext(ctx, "GetEndpoint failed during delivery",
			"delivery_id", d.ID,
			"endpoint_id", d.EndpointID,
			"error", err,
		)
		return
	}

	ciphertext, decErr := hex.DecodeString(ep.SecretEnc)
	if decErr != nil {
		slog.ErrorContext(ctx, "decode encrypted secret failed",
			"delivery_id", d.ID,
			"endpoint_id", ep.ID,
			"error", decErr,
		)
		return
	}
	hmacKeyBytes, err := aes.Decrypt(ciphertext, w.credentialKey)
	if err != nil {
		slog.ErrorContext(ctx, "decrypt HMAC secret failed",
			"delivery_id", d.ID,
			"endpoint_id", ep.ID,
			"error", err,
		)
		return
	}

	deliveryCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()

	err = w.dispatcher.Deliver(deliveryCtx, ep.URL, d.Payload, hmacKeyBytes)
	if err == nil {
		if markErr := w.repo.MarkDelivered(ctx, d.ID); markErr != nil {
			slog.ErrorContext(ctx, "MarkDelivered failed", "delivery_id", d.ID, "error", markErr)
		}
		slog.InfoContext(ctx, "webhook delivered",
			"delivery_id", d.ID,
			"endpoint_id", ep.ID,
			"url", ep.URL,
		)
		return
	}

	slog.WarnContext(ctx, "webhook delivery failed",
		"delivery_id", d.ID,
		"endpoint_id", ep.ID,
		"attempts", d.Attempts+1,
		"error", err,
	)

	nextAt, hasNext := delivery.NextRetryAt(d.Attempts + 1)
	dead := !hasNext
	if markErr := w.repo.MarkFailed(ctx, d.ID, err.Error(), nextAt, dead); markErr != nil {
		slog.ErrorContext(ctx, "MarkFailed failed", "delivery_id", d.ID, "error", markErr)
	}
	if dead {
		slog.ErrorContext(ctx, "webhook delivery exhausted retries — moved to dead",
			"delivery_id", d.ID,
			"endpoint_id", ep.ID,
			"url", ep.URL,
		)
	}
}

// EventRoutingKeys lists all RabbitMQ routing keys the webhook service subscribes to.
func EventRoutingKeys() []string {
	return []string{
		events.RoutingPushCompleted,
		events.RoutingManifestDeleted,
		events.RoutingTagDeleted,
		events.RoutingScanCompleted,
		events.RoutingScanPolicyBlocked,
		events.RoutingImageSigned,
	}
}

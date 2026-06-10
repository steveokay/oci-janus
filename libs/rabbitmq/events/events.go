// Package events defines all RabbitMQ event types for the registry platform.
// Every service that publishes or consumes events must use these types —
// never define private event types inside individual services.
package events

import (
	"encoding/json"
	"time"
)

// Routing keys — all events are published to the registry.events topic exchange.
const (
	RoutingPushCompleted      = "push.completed"
	RoutingPushFailed         = "push.failed"
	RoutingManifestDeleted    = "manifest.deleted"
	RoutingTagDeleted         = "tag.deleted"
	RoutingScanQueued         = "scan.queued"
	RoutingScanCompleted      = "scan.completed"
	RoutingScanPolicyBlocked  = "scan.policy_blocked"
	RoutingWebhookQueued      = "webhook.queued"
	RoutingWebhookDelivered   = "webhook.delivered"
	RoutingWebhookFailed      = "webhook.failed"
	RoutingGCRunStarted       = "gc.run.started"
	RoutingGCRunCompleted     = "gc.run.completed"
	RoutingImageSigned        = "image.signed"
	RoutingTenantCreated      = "tenant.created"
	RoutingTenantDomainVerified = "tenant.domain.verified"
	RoutingStoreQueued        = "store.queued" // proxy background store
)

// Exchange names
const (
	ExchangeEvents = "registry.events"
	ExchangeDLX    = "registry.dlx"
)

// Event is the envelope for all RabbitMQ messages.
type Event struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	TenantID   string          `json:"tenant_id"`
	OccurredAt time.Time       `json:"occurred_at"`
	Version    string          `json:"version"`
	Payload    json.RawMessage `json:"payload"`
}

// PushCompletedPayload is the payload for push.completed events.
type PushCompletedPayload struct {
	RepositoryName string `json:"repository_name"`
	RepoID         string `json:"repo_id"`
	Tag            string `json:"tag"`
	ManifestDigest string `json:"manifest_digest"`
	PushedBy       string `json:"pushed_by"`
	SizeBytes      int64  `json:"size_bytes"`
}

// ScanCompletedPayload is the payload for scan.completed events.
type ScanCompletedPayload struct {
	ManifestDigest  string         `json:"manifest_digest"`
	RepositoryName  string         `json:"repository_name"`
	ScannerName     string         `json:"scanner_name"`
	SeverityCounts  map[string]int `json:"severity_counts"`
	PolicyViolation bool           `json:"policy_violation"`
	Blocked         bool           `json:"blocked"`
}

// StoreQueuedPayload is the payload for store.queued events (proxy background store).
type StoreQueuedPayload struct {
	TenantID       string `json:"tenant_id"`
	UpstreamName   string `json:"upstream_name"`
	ManifestDigest string `json:"manifest_digest"`
	RepositoryName string `json:"repository_name"`
	Tag            string `json:"tag"`
}

// GCRunStartedPayload is the payload for gc.run.started events.
type GCRunStartedPayload struct {
	Mode string `json:"mode"`
}

// GCRunCompletedPayload is the payload for gc.run.completed events.
type GCRunCompletedPayload struct {
	Mode             string `json:"mode"`
	ManifestsDeleted int    `json:"manifests_deleted"`
	BlobsDeleted     int    `json:"blobs_deleted"`
	BytesFreed       int64  `json:"bytes_freed"`
	DryRun           bool   `json:"dry_run"`
}

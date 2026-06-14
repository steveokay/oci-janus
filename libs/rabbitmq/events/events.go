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

// StoreQueuedPayload is the payload for store.queued events.
// Published by registry-proxy when a background blob store fails so the
// consumer can retry with a fresh upstream fetch — no in-memory state needed.
type StoreQueuedPayload struct {
	TenantID       string `json:"tenant_id"`
	UpstreamName   string `json:"upstream_name"`
	// BlobDigest is the content-addressed sha256:... digest of the blob to (re-)store.
	BlobDigest     string `json:"blob_digest,omitempty"`
	// Image is the upstream image name (e.g. "library/ubuntu") used to re-fetch the blob.
	Image          string `json:"image,omitempty"`
	// The following fields are retained for manifest-level store events.
	ManifestDigest string `json:"manifest_digest,omitempty"`
	RepositoryName string `json:"repository_name,omitempty"`
	Tag            string `json:"tag,omitempty"`
}

// ScanQueuedPayload is the payload for scan.queued events.
// Published by registry-management when a user manually triggers a scan via the API,
// and consumed by registry-scanner to enqueue a scan job outside the normal push.completed flow.
type ScanQueuedPayload struct {
	TenantID       string `json:"tenant_id"`
	RepositoryName string `json:"repository_name"` // "org/repo"
	RepoID         string `json:"repo_id"`
	TagName        string `json:"tag_name"`
	ManifestDigest string `json:"manifest_digest"`
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

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
	RoutingPushCompleted        = "push.completed"
	RoutingPushFailed           = "push.failed"
	RoutingManifestDeleted      = "manifest.deleted"
	RoutingTagDeleted           = "tag.deleted"
	RoutingScanQueued           = "scan.queued"
	RoutingScanCompleted        = "scan.completed"
	RoutingScanPolicyBlocked    = "scan.policy_blocked"
	RoutingWebhookQueued        = "webhook.queued"
	RoutingWebhookDelivered     = "webhook.delivered"
	RoutingWebhookFailed        = "webhook.failed"
	RoutingGCRunStarted         = "gc.run.started"
	RoutingGCRunCompleted       = "gc.run.completed"
	RoutingImageSigned          = "image.signed"
	RoutingTenantCreated        = "tenant.created"
	RoutingTenantDeleted        = "tenant.deleted"
	RoutingTenantRenamed        = "tenant.renamed"       // FE-API-029
	RoutingTenantPlanChanged    = "tenant.plan_changed"  // FE-API-029
	RoutingTenantDomainVerified = "tenant.domain.verified"
	RoutingStoreQueued          = "store.queued" // proxy background store
	// RBAC audit events — consumed by registry-audit to record membership changes.
	RoutingRBACRoleGranted = "rbac.role_granted"
	RoutingRBACRoleRevoked = "rbac.role_revoked"

	// Retention events (FE-API-041) — published by services/gc's retention
	// executor so audit + webhook + dashboards can observe a sweep without
	// polling gc_runs. Three keys mirror the executor lifecycle:
	//
	//   evaluated       — start of a sweep, carries the would-delete totals.
	//                     Subscribers see "this is about to happen" before
	//                     any state changes. Also fires during a preview
	//                     window so operators can confirm the projection.
	//   applied         — soft-delete sweep finished and stamped at least
	//                     one manifest with retention_pending_delete_at.
	//   grace_completed — finaliser sweep hard-deleted manifests that have
	//                     ridden out the grace window.
	RoutingRetentionEvaluated      = "retention.evaluated"
	RoutingRetentionApplied        = "retention.applied"
	RoutingRetentionGraceCompleted = "retention.grace_completed"
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

// RoleGrantedPayload is the payload for rbac.role_granted events.
// Published by registry-auth when a role assignment is created so registry-audit
// can record the change without a direct gRPC dependency on the auth service.
// Never include passwords, tokens, or secret values in this payload.
type RoleGrantedPayload struct {
	TenantID   string `json:"tenant_id"`
	UserID     string `json:"user_id"`
	Role       string `json:"role"`
	ScopeType  string `json:"scope_type"`
	ScopeValue string `json:"scope_value"`
	GrantedBy  string `json:"granted_by"`
}

// RoleRevokedPayload is the payload for rbac.role_revoked events.
// Published by registry-auth when a role assignment is deleted.
type RoleRevokedPayload struct {
	TenantID     string `json:"tenant_id"`
	AssignmentID string `json:"assignment_id"`
	RevokedBy    string `json:"revoked_by"`
}

// ImageSignedPayload is the payload for image.signed events.
//
// Published by registry-management when a user signs a tag from the
// dashboard (FE-API-026). The signer service does not currently publish
// this event on its own when a sign succeeds, so management owns the
// event surface for now. If signer later publishes too, consumers must
// stay idempotent on (tenant_id, manifest_digest, signer_id, signed_at).
type ImageSignedPayload struct {
	TenantID        string `json:"tenant_id"`
	RepositoryName  string `json:"repository_name"`
	Tag             string `json:"tag"`
	ManifestDigest  string `json:"manifest_digest"`
	SignerID        string `json:"signer_id"`
	KeyID           string `json:"key_id"`
	SignatureDigest string `json:"signature_digest"`
	SignedBy        string `json:"signed_by"` // user_id of the caller
}

// RetentionEvaluatedPayload is the wire shape of retention.evaluated.
//
// Mirrors the run summary plus the would-delete totals so a subscriber
// can render "X manifests / Y bytes would be deleted" without a callback.
// Fires at the START of a retention sweep — before any manifests are
// marked — so subscribers see "this is about to happen". When the policy
// is in a preview window the executor still emits this event with
// PolicyPreviewUntil set so subscribers see "we would have done X but
// we're in preview". Mode is "retention" for soft-delete sweeps and
// "retention_grace" for finaliser sweeps.
type RetentionEvaluatedPayload struct {
	RunID              string     `json:"run_id"`
	TenantID           string     `json:"tenant_id"`
	RepositoryID       string     `json:"repository_id"`
	Mode               string     `json:"mode"`        // "retention" | "retention_grace"
	EvaluatedAt        time.Time  `json:"evaluated_at"`
	WouldDeleteCount   int64      `json:"would_delete_count"`
	WouldDeleteBytes   int64      `json:"would_delete_bytes"`
	PolicyPreviewUntil *time.Time `json:"policy_preview_until,omitempty"` // when in preview window
	TriggeredBy        string     `json:"triggered_by"`                   // "cron" or user_id
}

// RetentionAppliedPayload is the wire shape of retention.applied.
//
// Fires after a successful soft-delete sweep that stamped at least one
// manifest's retention_pending_delete_at column. The grace window starts
// counting from CompletedAt — subscribers building "manifests pending
// deletion" UIs can derive the hard-delete ETA from this timestamp plus
// the configured grace window.
type RetentionAppliedPayload struct {
	RunID               string    `json:"run_id"`
	TenantID            string    `json:"tenant_id"`
	RepositoryID        string    `json:"repository_id"`
	CompletedAt         time.Time `json:"completed_at"`
	ManifestsMarked     int64     `json:"manifests_marked"`
	ManifestsConsidered int64     `json:"manifests_considered"`
	TriggeredBy         string    `json:"triggered_by"`
}

// RetentionGraceCompletedPayload is the wire shape of
// retention.grace_completed.
//
// Fires after a successful finaliser sweep. BlobsFreed will normally be
// zero because the orphan-blob sweep runs separately and owns the blob
// counter — but the field exists so a future executor change can
// populate it without a payload migration.
type RetentionGraceCompletedPayload struct {
	RunID            string    `json:"run_id"`
	TenantID         string    `json:"tenant_id"`
	CompletedAt      time.Time `json:"completed_at"`
	ManifestsDeleted int64     `json:"manifests_deleted"`
	BlobsFreed       int64     `json:"blobs_freed"`
	BytesFreed       int64     `json:"bytes_freed"`
	TriggeredBy      string    `json:"triggered_by"`
}

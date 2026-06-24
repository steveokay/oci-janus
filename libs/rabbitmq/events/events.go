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

	// RoutingCachePopulated (FUT-017) — emitted by services/proxy after
	// every successful cache write (cacheManifest upsert). Subscribed by
	// services/scanner (queues a scan when the tenant's per-upstream
	// proxy_cache scan policy says auto-scan) and services/signer (auto-
	// signs with the workspace's default key when the per-upstream sign
	// policy says auto-sign). The payload is intentionally rich enough
	// that consumers don't need to call back into services/proxy for
	// follow-up reads — the routing key is the only shared concept.
	RoutingCachePopulated = "cache.populated"
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

	// RoutingPullImage (FE-API-042) fires from services/core after a successful
	// manifest GET. Carries the manifest identity + actor so services/audit can
	// record one audit_events row per pull (closing the FE-API-030 analytics
	// `metric=pulls` gap) and services/metadata can debounce-update
	// manifests.last_pulled_at for the FE-API-043 max_idle_days retention rule.
	//
	// Sampling is controlled per-publisher via PULL_EVENT_SAMPLE_RATE on
	// services/core — analytics precision degrades proportionally when sampling
	// is < 1.0, but the 24h debounce on metadata keeps last_pulled_at accurate
	// to within a day regardless of sample rate so long as it is > 0.
	RoutingPullImage = "pull.image"

	// RoutingServiceAccountLifecycle (FE-API-048 FUT-007) carries every SA
	// mutation emitted by services/auth's ServiceAccountService — created,
	// updated, disabled, enabled, deleted, key_issued, key_revoked,
	// scopes_updated, and the rbac.role_granted_to_service_account variant
	// described in spec §5.7. Subscribers branch on the embedded Action
	// field rather than the routing key so we don't have nine fan-out
	// keys to maintain. registry-audit's eventconsumer translates these
	// to audit_events rows with action == payload.Action so the activity
	// feed (FE-API-048 FUT-005) surfaces SA lifecycle alongside push/pull.
	RoutingServiceAccountLifecycle = "service_account.lifecycle"
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
	// S-MAINT-1 Batch 5 (P6): derived artifact-type discriminator
	// ("image" | "helm" | "signature" | "sbom" | "other"). Lets the
	// scanner skip non-image artifacts before enqueueing a Trivy/Grype
	// job that wouldn't find packages in a Helm chart / cosign sig.
	// Empty when the publisher didn't populate it — older publishers
	// stay compatible; consumer treats empty as "unknown — scan anyway"
	// so the scanner stays correct against pre-Batch-5 deployments.
	ArtifactType string `json:"artifact_type,omitempty"`
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

// CachePopulatedPayload is the payload for cache.populated events.
//
// Emitted by services/proxy after a successful UpsertManifest on the
// pull-through path. Subscribed by services/scanner (FUT-017 scan-on-
// cached-images) and services/signer (FUT-017 auto-sign-on-cache).
//
// All consumers do their own policy lookup keyed on (tenant_id,
// upstream_name) — the event itself doesn't carry policy state.
//
// ManifestDigest IS the per-arch manifest's digest (sha256:...). For
// manifest indexes, the proxy publishes one event per platform-manifest
// upsert; consumers therefore see CVE/sign results per arch even for
// multi-arch images.
type CachePopulatedPayload struct {
	TenantID       string `json:"tenant_id"`
	UpstreamID     string `json:"upstream_id"`
	UpstreamName   string `json:"upstream_name"`
	Image          string `json:"image"`
	Reference      string `json:"reference"`
	ManifestDigest string `json:"manifest_digest"`
	MediaType      string `json:"media_type"`
	SizeBytes      int64  `json:"size_bytes"`
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

// PullImagePayload is the wire shape of pull.image (FE-API-042).
//
// Published by services/core after a successful manifest GET. Two consumers
// land it today:
//   - services/audit writes one audit_events row per pull (action=pull.image)
//     so the FE-API-030 analytics `metric=pulls` query returns real bucket
//     counts instead of zeros.
//   - services/metadata debounce-updates manifests.last_pulled_at (at most
//     one Postgres write per (manifest, 24h)) so the FE-API-043 max_idle_days
//     retention rule has a column to evaluate.
//
// ManifestID is the metadata service's internal UUID — services/core does not
// always have it cached, so the field is optional. The consumer in
// services/metadata MUST fall back to (repo_id, manifest_digest) lookup when
// ManifestID is empty.
//
// Tag is non-empty only when the GET resolved a tag name (vs a digest-direct
// pull). Audit + analytics use it to attribute pulls to specific tags.
//
// ActorID is the user_id UUID from the JWT; empty when the pull came in via
// an anonymous public-pull path (the JWT carried no `sub`). Operators that
// want IP / UA attribution should subscribe to the matching webhook delivery
// — this payload deliberately avoids carrying request-level identifiers.
type PullImagePayload struct {
	TenantID       string    `json:"tenant_id"`
	RepositoryID   string    `json:"repository_id"`
	RepositoryName string    `json:"repository_name"` // "org/repo" composite
	ManifestDigest string    `json:"manifest_digest"`
	ManifestID     string    `json:"manifest_id,omitempty"`
	Tag            string    `json:"tag,omitempty"`
	ActorID        string    `json:"actor_id,omitempty"`
	PulledAt       time.Time `json:"pulled_at"`
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

// ServiceAccountLifecyclePayload is the wire shape of every SA mutation
// (FE-API-048 FUT-007). All routes through a single routing key
// (RoutingServiceAccountLifecycle); the embedded Action field identifies
// the sub-action — e.g. "service_account.created", "service_account.disabled",
// "service_account.key_issued". Spec §5.7 documents the full vocabulary.
//
// ActorID is the human admin who triggered the mutation (string-form UUID).
// Resource is the SA's id; populated on every action so audit dashboards
// can group rows per-SA without parsing Fields. Fields carries the
// action-specific extras (creator snapshot, scope diffs, key prefixes, …).
//
// TenantID lives on the outer Event envelope rather than this payload so
// consumers can route per-tenant without an extra unmarshal step.
type ServiceAccountLifecyclePayload struct {
	Action   string         `json:"action"`
	ActorID  string         `json:"actor_id"`
	Resource string         `json:"resource"`
	Fields   map[string]any `json:"fields,omitempty"`
}

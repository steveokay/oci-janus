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
	RoutingTenantRenamed        = "tenant.renamed"      // FE-API-029
	RoutingTenantPlanChanged    = "tenant.plan_changed" // FE-API-029
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

	// FUT-001 — federated workload identity. Three admin mutation events
	// + two exchange events. All five land in audit_events via the
	// eventconsumer (CLAUDE.md §10) so operators see trust changes and
	// every workload token exchange + rejection in /activity.
	RoutingOIDCTrustCreated       = "auth.oidc_trust.created"
	RoutingOIDCTrustUpdated       = "auth.oidc_trust.updated"
	RoutingOIDCTrustDeleted       = "auth.oidc_trust.deleted"
	RoutingWorkloadTokenExchanged = "auth.workload_token.exchanged"
	RoutingWorkloadTokenRejected  = "auth.workload_token.rejected"

	// FUT-003 — workspace-wide token policy.
	//
	// RoutingTokenPolicyChanged fires from services/auth's TokenPolicyService
	// after a successful PutTokenPolicy. Carries the before/after diff so a
	// subscriber can render "max_ttl_days: 90 → 60" without a callback.
	//
	// RoutingKeyRevoked fires from services/auth's ServiceAccountService (on
	// manual revoke) and from the FUT-003 idle-revoke background worker.
	// Reason distinguishes "manual" from "idle_revoked" from the FUT-004-
	// reserved "rotation_lapsed" — the audit consumer surfaces it as
	// metadata.reason so the activity feed can filter.
	RoutingTokenPolicyChanged = "auth.token_policy.changed" //nolint:gosec // G101 false positive: RabbitMQ routing key, not a credential (REM-014)
	RoutingKeyRevoked         = "auth.key_revoked"

	// FUT-004 — access review.
	//
	// RoutingAccessReviewDue fires from the weekly access-review worker
	// in services/auth once per stale API key surfaced during a tick.
	// Carries the reason ("idle" | "rotation_lapsed" | "both") so the
	// audit feed + notification bell can render "Key X due for review —
	// idle 92 days" without a callback into services/auth.
	//
	// RoutingAccessReviewSnoozed fires from AccessReviewService.SnoozeAPIKeyReview
	// after a successful snooze. Records the operator's explicit deferral
	// so the audit trail links the operator (actor) to the snooze in a
	// way that survives the key row's later revocation.
	//
	// Both are nudge-only events — neither one causes a mutation on any
	// other service; they exist purely for the audit + notification
	// surfaces.
	RoutingAccessReviewDue     = "auth.access_review.due"
	RoutingAccessReviewSnoozed = "auth.access_review.snoozed"

	// FUT-020 — image promotion.
	//
	// RoutingImagePromoted fires from services/management's BFF handler
	// after a successful metadata.PromoteTag call. Carries the full
	// promotion identity (src + dst side + digests + actor + note) so the
	// audit consumer + notification bell + webhook receiver can render the
	// event without a callback into services/metadata.
	//
	// Publish-side is BFF, not metadata, because emitting from inside the
	// tx would deliver a "promotion happened" event for a promotion that
	// might not actually commit — waiting until after Commit but before
	// return keeps the durable state authoritative. Publish failure is
	// logged but does NOT fail the response — the promotion is already
	// durable and audit can be replayed from the promotions table.
	RoutingImagePromoted = "image.promoted"

	// FUT-021 — CVSS-gated admission policy.
	//
	// RoutingRepoCVSSPolicyChanged fires from services/management's BFF
	// after a successful metadata.UpdateRepositoryCVSSPolicy call. The
	// audit consumer records the before/after threshold so the audit
	// trail can answer "who slackened the policy on prod-repo?".
	// Publish failure is logged but does NOT fail the response — the
	// durable state is already updated. Same posture as image.promoted.
	RoutingRepoCVSSPolicyChanged = "repo.cvss_policy.changed"

	// FUT-023 — ephemeral PR-scoped registries.
	//
	// RoutingPRNamespaceProvisioned fires from services/metadata after a
	// per-PR org namespace is created for an ephemeral registry.
	//
	// RoutingPRNamespaceTornDown fires from services/metadata when the
	// namespace is torn down — either the PR closed without merge, or it
	// merged and its images were promoted into a durable target org (the
	// payload's Promoted + TargetOrg fields distinguish the two).
	//
	// Both land in audit_events via the eventconsumer (CLAUDE.md §10) so
	// operators see the full lifecycle of every ephemeral PR registry in
	// /activity.
	RoutingPRNamespaceProvisioned = "pr.namespace.provisioned"
	RoutingPRNamespaceTornDown    = "pr.namespace.torn_down"
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
	TenantID     string `json:"tenant_id"`
	UpstreamName string `json:"upstream_name"`
	// BlobDigest is the content-addressed sha256:... digest of the blob to (re-)store.
	BlobDigest string `json:"blob_digest,omitempty"`
	// Image is the upstream image name (e.g. "library/ubuntu") used to re-fetch the blob.
	Image string `json:"image,omitempty"`
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
	Mode               string     `json:"mode"` // "retention" | "retention_grace"
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
// Published by two services today:
//   - services/core after a successful manifest GET on an owned repository.
//   - services/proxy after a successful manifest GET or HEAD on a cached
//     upstream image (FUT-014). HEAD counts as a pull because the Docker
//     client's HEAD-then-skip-GET path against a cached digest is the
//     dominant traffic shape against the proxy.
//
// Two consumers land it today:
//   - services/audit writes one audit_events row per pull (action=pull.image)
//     so the FE-API-030 analytics `metric=pulls` query returns real bucket
//     counts instead of zeros.
//   - services/metadata debounce-updates manifests.last_pulled_at (at most
//     one Postgres write per (manifest, 24h)) so the FE-API-043 max_idle_days
//     retention rule has a column to evaluate. The metadata consumer drops
//     events with an empty RepositoryID — that path is how proxy-emitted
//     pulls are silently ignored (proxy manifests don't live in
//     metadata.manifests).
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
//
// Via identifies the publishing service so analytics that want to break out
// owned-push pulls from pull-through-cache pulls can group by source.
// Empty / absent means services/core (owned-pull). "proxy" means
// services/proxy (cache-served pull). Consumers MUST treat unknown values
// as opaque so a new publisher can be added without a payload migration.
type PullImagePayload struct {
	TenantID       string    `json:"tenant_id"`
	RepositoryID   string    `json:"repository_id"`
	RepositoryName string    `json:"repository_name"` // "org/repo" composite
	ManifestDigest string    `json:"manifest_digest"`
	ManifestID     string    `json:"manifest_id,omitempty"`
	Tag            string    `json:"tag,omitempty"`
	ActorID        string    `json:"actor_id,omitempty"`
	PulledAt       time.Time `json:"pulled_at"`
	Via            string    `json:"via,omitempty"`
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

// OIDCTrustPayload is the wire shape of the FUT-001 trust admin events
// (created / updated / deleted). Carries the full trust identity so a
// subscriber can render the audit row without a callback into auth.
//
// ActorID is the admin who made the change — empty for events emitted
// by the cascade-delete-on-SA-delete path (the deleter is implicit).
type OIDCTrustPayload struct {
	TrustID          string `json:"trust_id"`
	TenantID         string `json:"tenant_id"`
	ServiceAccountID string `json:"service_account_id"`
	DisplayName      string `json:"display_name"`
	IssuerURL        string `json:"issuer_url"`
	Audience         string `json:"audience"`
	SubjectPattern   string `json:"subject_pattern"`
	ActorID          string `json:"actor_id,omitempty"`
}

// WorkloadTokenPayload is the wire shape of the FUT-001 exchange events
// (exchanged / rejected). TrustID is empty on rejections that happened
// before a trust could be matched (issuer not in allowlist, audience
// mismatch). Reason is set ONLY on rejection events; the exchanged
// event leaves it empty.
//
// Subject is the OIDC `sub` claim, truncated to 256 chars by the
// emitter so a hostile caller cannot inflate audit row size.
type WorkloadTokenPayload struct {
	TrustID          string `json:"trust_id,omitempty"`
	IssuerURL        string `json:"issuer_url"`
	Subject          string `json:"subject"`
	ServiceAccountID string `json:"service_account_id,omitempty"`
	Reason           string `json:"reason,omitempty"`
}

// FUT-003 payloads ─────────────────────────────────────────────────────

// PolicySnapshot is a compact snapshot of a token_policies row used in
// TokenPolicyChangedPayload's before/after diff. Nil pointer fields mean
// "no cap for that dimension" (NULL in DB); the audit consumer preserves
// nil vs zero so a subscriber can render "unset → 90 days" distinctly
// from "0 days → 90 days" (the latter is impossible today — the service
// rejects zero — but preserving the shape leaves the door open).
type PolicySnapshot struct {
	MaxTTLDays           *int32 `json:"max_ttl_days,omitempty"`
	RotationIntervalDays *int32 `json:"rotation_interval_days,omitempty"`
	IdleRevokeDays       *int32 `json:"idle_revoke_days,omitempty"`
}

// TokenPolicyChangedPayload is the wire shape of auth.token_policy.changed.
// Fires after a successful PutTokenPolicy. Before is the state just before
// the mutation (all-nil when the tenant had no policy row); After is the
// state persisted by the same call. ActorID is the admin who made the
// change (from the JWT sub).
type TokenPolicyChangedPayload struct {
	TenantID string         `json:"tenant_id"`
	ActorID  string         `json:"actor_id"`
	Before   PolicySnapshot `json:"before"`
	After    PolicySnapshot `json:"after"`
}

// KeyRevokedPayload is the wire shape of auth.key_revoked. Fires from
// manual admin revoke paths AND from the FUT-003 idle-revoke background
// worker. Reason is one of "manual" | "idle_revoked" | "rotation_lapsed"
// (the last is reserved for FUT-004). OwnerUserID is the human user for
// human-owned keys or the SA's shadow user id for SA-owned keys — the
// audit consumer's actor gate treats both the same.
type KeyRevokedPayload struct {
	TenantID    string `json:"tenant_id"`
	KeyID       string `json:"key_id"`
	OwnerUserID string `json:"owner_user_id"`
	Reason      string `json:"reason"`
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

// FUT-004 payloads ─────────────────────────────────────────────────────

// AccessReviewDuePayload is the wire shape of auth.access_review.due.
// Fires from the weekly worker once per stale key surfaced during a
// tick. Reason distinguishes "idle" (last_used_at past cutoff) from
// "rotation_lapsed" (rotation_due_at in past) from "both". OwnerUserID
// is the human user for human-owned keys or the SA's shadow user id
// for SA-owned keys — the audit consumer's actor gate treats both the
// same. DaysIdle is optional (zero when not applicable, e.g. a
// rotation-only lapse on a fresh key).
type AccessReviewDuePayload struct {
	TenantID    string `json:"tenant_id"`
	KeyID       string `json:"key_id"`
	OwnerUserID string `json:"owner_user_id"`
	Name        string `json:"name"`
	Reason      string `json:"reason"` // "idle" | "rotation_lapsed" | "both"
	DaysIdle    int32  `json:"days_idle,omitempty"`
}

// ImagePromotedPayload is the wire shape of image.promoted (FUT-020).
//
// Captures the full promotion identity at emit time so the audit + webhook
// consumers don't need to callback into services/metadata. src_digest and
// dst_digest are stamped separately so a future re-sign / retag workflow
// where the destination diverges from the source has a clean data path.
// Today src_digest == dst_digest by design (see the promotions table
// migration for details).
//
// ActorUserID is empty on CLI / bot-driven promotions where the API-key
// owner is a service account with no human user attribution. The audit
// consumer treats empty as "system" for the actor_type column so the
// activity feed has a stable value rather than a blank cell.
type ImagePromotedPayload struct {
	TenantID    string `json:"tenant_id"`
	SrcOrg      string `json:"src_org"`
	SrcRepo     string `json:"src_repo"`
	SrcTag      string `json:"src_tag"`
	SrcDigest   string `json:"src_digest"`
	DstOrg      string `json:"dst_org"`
	DstRepo     string `json:"dst_repo"`
	DstTag      string `json:"dst_tag"`
	DstDigest   string `json:"dst_digest"`
	ActorUserID string `json:"actor_user_id,omitempty"`
	Note        string `json:"note,omitempty"`
}

// RepoCVSSPolicyChangedPayload is the wire shape of
// repo.cvss_policy.changed (FUT-021).
//
// Before / After are pointer *int32 so nil renders as JSON `null` on
// the wire — that's how the audit consumer distinguishes "gate was
// cleared" from "gate set to 0". The audit event records both sides so
// the trail can render "70 → cleared" or "cleared → 90" transitions
// with the same event shape.
//
// ActorID may be empty when the change came from a service-account
// API key that isn't attached to a shadow user (rare, but possible on
// legacy keys). The audit consumer treats empty as "system" for the
// actor_type column.
type RepoCVSSPolicyChangedPayload struct {
	TenantID string `json:"tenant_id"`
	Org      string `json:"org"`
	Repo     string `json:"repo"`
	ActorID  string `json:"actor_id,omitempty"`
	Before   *int32 `json:"before,omitempty"`
	After    *int32 `json:"after,omitempty"`
}

// FUT-023 payloads ─────────────────────────────────────────────────────

// PRNamespaceProvisionedPayload is the wire shape of
// pr.namespace.provisioned (FUT-023 Phase 1).
//
// Emitted by services/metadata after a per-PR org namespace is created
// for an ephemeral registry. Carries the full provisioning identity so
// the audit consumer can render the audit row (and a future teardown
// reconciler can match on OrgName) without a callback into
// services/metadata. OrgName is the synthesised per-PR namespace
// (e.g. "pr-1234-<repo>").
type PRNamespaceProvisionedPayload struct {
	TenantID   string `json:"tenant_id"`
	Provider   string `json:"provider"`
	SourceRepo string `json:"source_repo"`
	PRNumber   int    `json:"pr_number"`
	OrgName    string `json:"org_name"`
}

// PRNamespaceTornDownPayload is the wire shape of pr.namespace.torn_down
// (FUT-023 Phase 1).
//
// Emitted by services/metadata when a per-PR org namespace is torn down —
// either because the PR closed without merge (Promoted false) or because
// the PR merged and its artifacts were promoted into a durable target org
// (Promoted true, TargetOrg set). Carrying the promotion outcome lets the
// audit feed distinguish "PR abandoned, namespace GC'd" from "PR merged,
// images promoted to <target>".
type PRNamespaceTornDownPayload struct {
	TenantID   string `json:"tenant_id"`
	Provider   string `json:"provider"`
	SourceRepo string `json:"source_repo"`
	PRNumber   int    `json:"pr_number"`
	OrgName    string `json:"org_name"`
	Promoted   bool   `json:"promoted"`             // true when merged + target promoted
	TargetOrg  string `json:"target_org,omitempty"` // set when Promoted
}

// AccessReviewSnoozedPayload is the wire shape of auth.access_review.snoozed.
// Fires from AccessReviewService.SnoozeAPIKeyReview after a successful
// snooze so the audit trail records who deferred the review + until when.
// SnoozedUntil is RFC3339-formatted so the audit consumer can render it
// without a parse step; DaysSnoozed carries the operator-picked window
// so the FE (and analytics) can distinguish 30-day vs 90-day snoozes.
type AccessReviewSnoozedPayload struct {
	TenantID     string `json:"tenant_id"`
	KeyID        string `json:"key_id"`
	ActorID      string `json:"actor_id"`
	SnoozedUntil string `json:"snoozed_until"`
	DaysSnoozed  int32  `json:"days_snoozed"`
}

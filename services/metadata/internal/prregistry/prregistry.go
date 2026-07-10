// Package prregistry implements the business logic for FUT-023 Phase 1 —
// ephemeral, PR-scoped registries.
//
// A GitHub PR webhook drives the create/promote/delete lifecycle of a
// per-PR org namespace (e.g. "pr-<repo>-<N>"). This package owns the four
// pure, DB/broker-free pieces of that flow so they are unit-testable in
// isolation:
//
//   - verify.go  — HMAC-SHA256 webhook signature verification (fail-closed,
//     constant-time) against the KEK-unsealed per-tenant secret.
//   - github.go  — GitHub PR webhook payload parsing (only the fields we act
//     on; unknown fields ignored).
//   - name.go    — namespace-name derivation to the org regex
//     `^[a-z0-9-]{2,64}$`, with middle-truncation of the repo portion.
//   - service.go — the opened/closed/merged lifecycle dispatch, including
//     promote-on-merge.
//
// The package depends only on narrow Store and Publisher interfaces (defined
// here) rather than the concrete *repository.Repository or the concrete
// RabbitMQ publisher, so a test can fake both without a database or a broker.
// The handler (a separate task) constructs the real Service by passing the
// real repository + publisher, and maps the package-local Outcome enum onto
// the proto HandlePREventResponse_OUTCOME_* values — this package stays free
// of proto types.
package prregistry

import (
	"context"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
)

// Store is the narrow slice of the metadata repository that the PR-registry
// lifecycle needs. Declaring it here (rather than depending on the concrete
// *repository.Repository) keeps the Service fakeable in unit tests. The real
// *repository.Repository satisfies every method verbatim.
//
// Absence of a config/namespace row is signalled by repository.ErrNotFound —
// callers match it with errors.Is.
type Store interface {
	// GetOrCreateOrganization idempotently upserts the ephemeral per-PR org
	// and returns its id. tenantID is passed as a string to match the shipped
	// repository signature.
	GetOrCreateOrganization(ctx context.Context, tenantID, orgName string) (orgID string, err error)

	// UpsertPRNamespace provisions (or re-provisions, on GitHub re-delivery)
	// the lifecycle row for a PR and returns the persisted row (with its
	// server-assigned id + org_id).
	UpsertPRNamespace(ctx context.Context, ns repository.PRNamespace) (*repository.PRNamespace, error)

	// GetPRNamespace resolves the lifecycle row by its unique key. Returns
	// repository.ErrNotFound when the PR was never provisioned. Required by
	// teardown because TearDownPRNamespace is keyed by namespace id, so we
	// must resolve the (tenant, provider, source_repo, pr_number) tuple to
	// an id + org_id first.
	GetPRNamespace(ctx context.Context, tenantID uuid.UUID, provider, sourceRepo string, prNumber int) (*repository.PRNamespace, error)

	// TearDownPRNamespace marks the namespace torn_down and deletes its
	// ephemeral org atomically, both scoped by tenantID (SEC-085 #3).
	// Idempotent — orgID may be uuid.Nil.
	TearDownPRNamespace(ctx context.Context, tenantID, namespaceID, orgID uuid.UUID) error

	// ListRepositories enumerates a org's repositories (artifactType "" ⇒ all)
	// so the promote-on-merge fan-out can walk them.
	ListRepositories(ctx context.Context, tenantID, orgID, artifactType string) ([]*metadatav1.Repository, error)

	// ListTags returns one page of a repository's tags (keyset paginated via
	// the `last` cursor).
	ListTags(ctx context.Context, tenantID, repoID string, pageSize int32, last string) ([]*metadatav1.Tag, error)

	// PromoteTag copies a source tag onto a destination org/repo/tag in one
	// transaction. Returns repository.ErrImmutableTag when the destination tag
	// is immutable — promote-on-merge logs + skips that tag.
	PromoteTag(ctx context.Context, in repository.PromoteTagInput) (*metadatav1.Promotion, error)
}

// Publisher is the narrow slice of the RabbitMQ publisher the Service uses.
// The concrete *publisher.Publisher satisfies this verbatim. Declaring it
// locally keeps the Service broker-free in tests (a fake records the
// (routingKey, Event) pairs).
type Publisher interface {
	Publish(ctx context.Context, routingKey string, evt events.Event) error
}

// Service wires the PR-registry lifecycle to its Store, Publisher, and the
// AES-256-GCM key-encryption key (KEK) used to unseal the per-tenant webhook
// secret. Construct it via New. A nil/empty kek disables the feature (Verify
// fails closed with ErrFeatureDisabled) so a deployment that never set
// PR_REGISTRY_KEY_HEX behaves as "integration off" rather than crashing.
type Service struct {
	store Store
	pub   Publisher
	kek   []byte
}

// New constructs a Service. kek is the raw 32-byte AES-256 key (or nil/empty
// when the feature is not configured — Verify then fails closed).
func New(store Store, pub Publisher, kek []byte) *Service {
	return &Service{store: store, pub: pub, kek: kek}
}

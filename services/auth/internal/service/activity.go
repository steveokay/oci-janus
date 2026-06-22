// Package service — ActivityService (FE-API-048, Task 11).
//
// ActivityService is a thin facade on top of the audit gRPC service that
// returns a principal's recent activity events. It enforces the ordering-of-
// checks from spec §5.3 so that cross-tenant probing and non-admin queries
// against other users both return an identical 404 shape — no timing oracle
// and no existence leak.
//
// Production wiring: the handler layer (T15) constructs an ActivityService with
// the shared auditv1.AuditServiceClient already held by services/auth and the
// same userRepo used by the rest of the service package.
package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// UserRepoForActivity is the minimal interface ActivityService needs from the
// user repository. Defining a narrow interface here (rather than reusing the
// full userRepo) keeps the dependency surface small and allows tests to supply
// a focused fake without implementing every userRepo method.
type UserRepoForActivity interface {
	// GetUserAnyKind returns the user with the given ID regardless of kind,
	// including service-account shadow users. Returns repository.ErrNotFound
	// when no row with that ID exists.
	GetUserAnyKind(ctx context.Context, id uuid.UUID) (*repository.User, error)
}

// ActivityService provides the business logic for the
// GET /access/activity endpoint. It resolves the target principal, enforces
// tenant isolation and non-admin access rules, then delegates to the audit
// gRPC service to fetch the event feed.
type ActivityService struct {
	// users provides principal lookup (human and shadow) without DB dependency
	// in tests.
	users UserRepoForActivity
	// audit is the gRPC client for services/audit. In tests a fakeAuditClient
	// is injected; in production the same client shared by the rest of
	// services/auth is used.
	audit auditv1.AuditServiceClient
}

// NewActivityService constructs an ActivityService.
// users must not be nil. audit must not be nil.
func NewActivityService(users UserRepoForActivity, audit auditv1.AuditServiceClient) *ActivityService {
	return &ActivityService{users: users, audit: audit}
}

// PrincipalActivity is one trimmed audit event in the principal's activity
// feed. Fields that are not directly useful to the frontend (event id, trace
// id, raw manifest digest) are omitted to keep the response narrow.
type PrincipalActivity struct {
	// At is the wall-clock time the event occurred.
	At time.Time
	// Action is the audit action code (e.g. "push.image", "pull.image").
	Action string
	// Repo is extracted from the event metadata["repo"] key when present.
	// Empty when the event is not repository-scoped.
	Repo string
	// SourceIP is the initiating IP address if present in metadata["source_ip"].
	SourceIP string
	// APIKeyID is the key UUID if the request was authenticated with an API key,
	// extracted from metadata["api_key_id"].
	APIKeyID string
	// Status is the audit outcome ("success" | "failure").
	Status string
}

// ListActivityOpts carries the caller-supplied parameters for List.
// CallerUserID and CallerTenantID are extracted from the authenticated
// request context by the handler; TargetUserID is the principal_user_id
// query parameter.
type ListActivityOpts struct {
	// CallerUserID is the authenticated caller's user ID.
	CallerUserID uuid.UUID
	// CallerTenantID is the authenticated caller's tenant ID.
	CallerTenantID uuid.UUID
	// CallerIsAdmin is true when the caller holds a workspace-admin role in
	// their tenant (checked by the handler before calling List).
	CallerIsAdmin bool
	// TargetUserID is the principal whose activity is being requested.
	TargetUserID uuid.UUID
	// PageSize limits the number of events returned. 0 ⇒ the audit service
	// default (typically 50). Hard-capped server-side by the audit service.
	PageSize int32
	// PageToken is the opaque cursor returned by a prior call.
	PageToken string
}

// List resolves the target principal, enforces tenant isolation and
// non-admin access rules, then fetches the event feed from the audit service.
//
// Order-of-checks (spec §5.3, security finding M4):
//  1. Resolve target via GetUserAnyKind (shadow users are valid targets).
//  2. Tenant check: target.TenantID != opts.CallerTenantID → 404.
//  3. Non-admin check: !CallerIsAdmin && target.ID != opts.CallerUserID → 404.
//  4. gRPC call to audit, return result.
//
// Both negative paths (2) and (3) return identical-shape 404 errors so that
// an attacker cannot distinguish "user in wrong tenant" from "user doesn't
// exist" by comparing status codes or messages.
//
// Returns (events, nextPageToken, error).
func (s *ActivityService) List(ctx context.Context, opts ListActivityOpts) ([]PrincipalActivity, string, error) {
	// Step 1: Resolve the target principal. GetUserAnyKind includes shadow users
	// so SA activity is queryable by an admin without special casing.
	target, err := s.users.GetUserAnyKind(ctx, opts.TargetUserID)
	if errors.Is(err, repository.ErrNotFound) || (err == nil && target.TenantID != opts.CallerTenantID) {
		// Steps 1+2 share the same return path intentionally: "not found" and
		// "wrong tenant" must be indistinguishable in shape, message, and timing.
		// The lookup always completes before we return so there is no early-exit
		// timing oracle (the short-circuit is the nil-err branch, not a skip).
		return nil, "", status.Error(codes.NotFound, "not found")
	}
	if err != nil {
		return nil, "", status.Error(codes.Internal, "lookup target")
	}

	// Step 3: Non-admin callers may only query their own activity.
	if !opts.CallerIsAdmin && target.ID != opts.CallerUserID {
		// Same 404 shape — never 403. Returning Forbidden leaks that the target
		// user exists in this tenant (security finding M4).
		return nil, "", status.Error(codes.NotFound, "not found")
	}

	// Step 4: Delegate to the audit service. GetNotifications is the available
	// tenant-scoped event feed; we filter client-side by actor_id because the
	// audit proto currently does not expose an actor_id request filter. The
	// tenant_id binding provides the primary isolation guarantee; the actor_id
	// filter narrows the feed to the target principal's events only.
	resp, err := s.audit.GetNotifications(ctx, &auditv1.GetNotificationsRequest{
		TenantId:  opts.CallerTenantID.String(),
		Limit:     opts.PageSize,
		PageToken: opts.PageToken,
	})
	if err != nil {
		return nil, "", err
	}

	// Trim the notification events to the target actor and project to the
	// PrincipalActivity shape. Events belonging to other actors are dropped
	// so that the caller only sees the requested principal's activity.
	activities := trimNotifications(resp.Notifications, target.ID.String())
	return activities, resp.NextPageToken, nil
}

// trimNotifications filters and projects audit NotificationEvent records to
// PrincipalActivity values. Events whose actor_id does not match actorID are
// silently dropped. Fields are extracted from the metadata map when present.
func trimNotifications(events []*auditv1.NotificationEvent, actorID string) []PrincipalActivity {
	// Pre-allocate for the common case where most events belong to the actor.
	out := make([]PrincipalActivity, 0, len(events))
	for _, ev := range events {
		// Filter to the requested actor only.
		if ev.GetActorId() != actorID {
			continue
		}

		var at time.Time
		if ts := ev.GetOccurredAt(); ts != nil {
			at = ts.AsTime()
		}

		// Extract optional metadata fields. The metadata map may be nil or
		// absent — all key lookups gracefully return the zero string.
		meta := ev.GetMetadata()
		out = append(out, PrincipalActivity{
			At:       at,
			Action:   ev.GetEventType(),
			Repo:     meta["repo"],
			SourceIP: meta["source_ip"],
			APIKeyID: meta["api_key_id"],
			Status:   meta["outcome"],
		})
	}
	return out
}

// FE-API-041 — verify the three retention.* routing keys are mapped to
// audit_events rows by the consumer. We exercise mapEvent directly (a
// pure function) so we don't need to stand up a real Repository.
package eventconsumer

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
)

// TestMapEvent_retentionRoutingKeys ensures all three retention events
// produce a non-nil AuditEvent with the right action string. A regression
// here would mean the audit feed silently drops retention events even
// though the consumer's subscription would still ACK them.
func TestMapEvent_retentionRoutingKeys(t *testing.T) {
	tenantID := uuid.New()
	repoID := uuid.New().String()
	occurred := time.Now().UTC()

	evalPayload, _ := json.Marshal(events.RetentionEvaluatedPayload{
		RunID:        uuid.NewString(),
		TenantID:     tenantID.String(),
		RepositoryID: repoID,
		Mode:         "retention",
		EvaluatedAt:  occurred,
		TriggeredBy:  "cron",
	})
	appliedPayload, _ := json.Marshal(events.RetentionAppliedPayload{
		RunID:           uuid.NewString(),
		TenantID:        tenantID.String(),
		RepositoryID:    repoID,
		CompletedAt:     occurred,
		ManifestsMarked: 5,
		TriggeredBy:     "cron",
	})
	gracePayload, _ := json.Marshal(events.RetentionGraceCompletedPayload{
		RunID:            uuid.NewString(),
		TenantID:         tenantID.String(),
		CompletedAt:      occurred,
		ManifestsDeleted: 3,
		TriggeredBy:      "cron",
	})

	cases := []struct {
		name       string
		eventType  string
		payload    json.RawMessage
		wantAction string
	}{
		{
			name:       "retention.evaluated",
			eventType:  events.RoutingRetentionEvaluated,
			payload:    evalPayload,
			wantAction: "retention.evaluated",
		},
		{
			name:       "retention.applied",
			eventType:  events.RoutingRetentionApplied,
			payload:    appliedPayload,
			wantAction: "retention.applied",
		},
		{
			name:       "retention.grace_completed",
			eventType:  events.RoutingRetentionGraceCompleted,
			payload:    gracePayload,
			wantAction: "retention.grace_completed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := events.Event{
				ID:         uuid.NewString(),
				Type:       tc.eventType,
				TenantID:   tenantID.String(),
				OccurredAt: occurred,
				Version:    "1.0",
				Payload:    tc.payload,
			}
			ae := mapEvent(tenantID, ev)
			if ae == nil {
				t.Fatalf("mapEvent(%q) returned nil — routing key not in allowlist", tc.eventType)
			}
			if ae.Action != tc.wantAction {
				t.Errorf("action: got %q, want %q", ae.Action, tc.wantAction)
			}
			if ae.TenantID != tenantID {
				t.Errorf("tenant_id: got %v, want %v", ae.TenantID, tenantID)
			}
			if ae.Outcome != "success" {
				t.Errorf("outcome: got %q, want \"success\"", ae.Outcome)
			}
		})
	}
}

// TestMapEvent_prNamespaceRoutingKeys (FUT-023) verifies both PR-registry
// lifecycle events map to system-actor audit_events rows whose resource is
// the synthesised per-PR org name. A regression here would silently drop
// ephemeral-registry provisioning/teardown from the audit trail.
func TestMapEvent_prNamespaceRoutingKeys(t *testing.T) {
	tenantID := uuid.New()
	occurred := time.Now().UTC()

	provisionedPayload, _ := json.Marshal(events.PRNamespaceProvisionedPayload{
		TenantID:   tenantID.String(),
		Provider:   "github",
		SourceRepo: "acme/widget",
		PRNumber:   1234,
		OrgName:    "pr-1234-widget",
	})
	tornDownPayload, _ := json.Marshal(events.PRNamespaceTornDownPayload{
		TenantID:   tenantID.String(),
		Provider:   "github",
		SourceRepo: "acme/widget",
		PRNumber:   1234,
		OrgName:    "pr-1234-widget",
		Promoted:   true,
		TargetOrg:  "acme",
	})

	cases := []struct {
		name         string
		eventType    string
		payload      json.RawMessage
		wantAction   string
		wantResource string
	}{
		{
			name:         "pr.namespace.provisioned",
			eventType:    events.RoutingPRNamespaceProvisioned,
			payload:      provisionedPayload,
			wantAction:   "pr.namespace.provisioned",
			wantResource: "pr-1234-widget",
		},
		{
			name:         "pr.namespace.torn_down",
			eventType:    events.RoutingPRNamespaceTornDown,
			payload:      tornDownPayload,
			wantAction:   "pr.namespace.torn_down",
			wantResource: "pr-1234-widget",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := events.Event{
				ID:         uuid.NewString(),
				Type:       tc.eventType,
				TenantID:   tenantID.String(),
				OccurredAt: occurred,
				Version:    "1.0",
				Payload:    tc.payload,
			}
			ae := mapEvent(tenantID, ev)
			if ae == nil {
				t.Fatalf("mapEvent(%q) returned nil — routing key not in allowlist", tc.eventType)
			}
			if ae.Action != tc.wantAction {
				t.Errorf("action: got %q, want %q", ae.Action, tc.wantAction)
			}
			if ae.Resource != tc.wantResource {
				t.Errorf("resource: got %q, want %q", ae.Resource, tc.wantResource)
			}
			if ae.ActorType != "system" {
				t.Errorf("actor_type: got %q, want system", ae.ActorType)
			}
			if ae.Outcome != "success" {
				t.Errorf("outcome: got %q, want success", ae.Outcome)
			}
		})
	}
}

// TestMapEvent_pullImage_authenticated verifies the FE-API-042 happy path:
// a pull with a known actor maps to a pull.image row with actor_type=user
// and resource = "org/repo:tag" so it groups with the push.image events.
func TestMapEvent_pullImage_authenticated(t *testing.T) {
	tenantID := uuid.New()
	payload, _ := json.Marshal(events.PullImagePayload{
		TenantID:       tenantID.String(),
		RepositoryID:   uuid.NewString(),
		RepositoryName: "myorg/myimage",
		ManifestDigest: "sha256:abc",
		Tag:            "v1.0.0",
		ActorID:        "user-42",
		PulledAt:       time.Now().UTC(),
	})
	ev := events.Event{
		ID:         uuid.NewString(),
		Type:       events.RoutingPullImage,
		TenantID:   tenantID.String(),
		OccurredAt: time.Now().UTC(),
		Version:    "1.0",
		Payload:    payload,
	}

	ae := mapEvent(tenantID, ev)
	if ae == nil {
		t.Fatalf("mapEvent returned nil for pull.image — allowlist regression")
	}
	if ae.Action != "pull.image" {
		t.Errorf("action: got %q, want pull.image", ae.Action)
	}
	if ae.ActorID != "user-42" {
		t.Errorf("actor_id: got %q, want user-42", ae.ActorID)
	}
	if ae.ActorType != "user" {
		t.Errorf("actor_type: got %q, want user", ae.ActorType)
	}
	if ae.Resource != "myorg/myimage:v1.0.0" {
		t.Errorf("resource: got %q, want myorg/myimage:v1.0.0", ae.Resource)
	}
	if ae.Outcome != "success" {
		t.Errorf("outcome: got %q, want success", ae.Outcome)
	}
}

// TestMapEvent_pullImage_anonymous verifies an anonymous public pull (no JWT)
// records actor_id="anonymous" so the dashboard's actor filter has a stable
// non-empty value rather than a blank cell. Resource falls back to
// "org/repo@sha256:..." when the GET resolved by digest (no tag in payload).
func TestMapEvent_pullImage_anonymous(t *testing.T) {
	tenantID := uuid.New()
	payload, _ := json.Marshal(events.PullImagePayload{
		TenantID:       tenantID.String(),
		RepositoryID:   uuid.NewString(),
		RepositoryName: "public/library",
		ManifestDigest: "sha256:def",
		// No Tag, no ActorID — anonymous pull-by-digest.
		PulledAt: time.Now().UTC(),
	})
	ev := events.Event{
		ID:         uuid.NewString(),
		Type:       events.RoutingPullImage,
		TenantID:   tenantID.String(),
		OccurredAt: time.Now().UTC(),
		Version:    "1.0",
		Payload:    payload,
	}

	ae := mapEvent(tenantID, ev)
	if ae == nil {
		t.Fatalf("mapEvent returned nil for anonymous pull.image")
	}
	if ae.ActorID != "anonymous" {
		t.Errorf("actor_id: got %q, want anonymous", ae.ActorID)
	}
	if ae.ActorType != "anonymous" {
		t.Errorf("actor_type: got %q, want anonymous", ae.ActorType)
	}
	if ae.Resource != "public/library@sha256:def" {
		t.Errorf("resource: got %q, want public/library@sha256:def", ae.Resource)
	}
}

// TestMapEvent_accessReview_malformedPayloadDropped (SEC-070) asserts the
// two FUT-004 cases drop a malformed payload (nil → ACK, no insert)
// instead of persisting a blank-Resource audit row. Well-formed payloads
// still map so the drop path can't mask an over-eager rejection.
func TestMapEvent_accessReview_malformedPayloadDropped(t *testing.T) {
	tenantID := uuid.New()
	garbage := json.RawMessage(`{"key_id": 42,`) // truncated + wrong type

	for _, eventType := range []string{
		events.RoutingAccessReviewDue,
		events.RoutingAccessReviewSnoozed,
	} {
		t.Run(eventType+"/malformed", func(t *testing.T) {
			ev := events.Event{
				ID:         uuid.NewString(),
				Type:       eventType,
				TenantID:   tenantID.String(),
				OccurredAt: time.Now().UTC(),
				Version:    "1.0",
				Payload:    garbage,
			}
			if ae := mapEvent(tenantID, ev); ae != nil {
				t.Errorf("mapEvent(%q) with malformed payload: got row (resource=%q), want nil drop",
					eventType, ae.Resource)
			}
		})
	}

	// Happy-path guard: a well-formed snoozed payload must still map.
	payload, _ := json.Marshal(events.AccessReviewSnoozedPayload{
		TenantID:     tenantID.String(),
		KeyID:        uuid.NewString(),
		ActorID:      uuid.NewString(),
		SnoozedUntil: time.Now().UTC().Format(time.RFC3339),
		DaysSnoozed:  30,
	})
	ev := events.Event{
		ID:         uuid.NewString(),
		Type:       events.RoutingAccessReviewSnoozed,
		TenantID:   tenantID.String(),
		OccurredAt: time.Now().UTC(),
		Version:    "1.0",
		Payload:    payload,
	}
	ae := mapEvent(tenantID, ev)
	if ae == nil {
		t.Fatalf("well-formed access_review.snoozed payload must still map")
	}
	if ae.Action != "auth.access_review.snoozed" {
		t.Errorf("action: got %q, want auth.access_review.snoozed", ae.Action)
	}
}

// TestMapEvent_saLifecycle_populatesActorIP verifies the SA lifecycle
// consumer copies the payload's SourceIP into AuditEvent.ActorIP so the
// row's actor_ip column carries the client IP for the activity feed.
func TestMapEvent_saLifecycle_populatesActorIP(t *testing.T) {
	tenantID := uuid.New()
	payload, _ := json.Marshal(events.ServiceAccountLifecyclePayload{
		Action:   "service_account.key_issued",
		ActorID:  "actor-1",
		Resource: "sa-1",
		SourceIP: "203.0.113.9",
		APIKeyID: "key-77",
	})
	ev := events.Event{
		ID:         uuid.NewString(),
		Type:       events.RoutingServiceAccountLifecycle,
		TenantID:   tenantID.String(),
		OccurredAt: time.Now().UTC(),
		Version:    "1.0",
		Payload:    payload,
	}

	ae := mapEvent(tenantID, ev)
	if ae == nil {
		t.Fatal("mapEvent returned nil for SA lifecycle event")
	}
	if ae.ActorIP != "203.0.113.9" {
		t.Errorf("ActorIP = %q, want 203.0.113.9", ae.ActorIP)
	}
	if ae.Action != "service_account.key_issued" {
		t.Errorf("Action = %q, want service_account.key_issued", ae.Action)
	}
}

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

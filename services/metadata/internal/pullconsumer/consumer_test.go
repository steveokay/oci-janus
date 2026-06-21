// FE-API-042 — consumer payload-validation coverage. The DB-touching paths
// (UpsertManifestLastPulledAt, FindManifestIDByDigest) are covered by the
// integration tests under services/metadata/internal/testutil/integration —
// this file pins the upstream filter logic without standing up Postgres.
package pullconsumer

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
)

// TestHandleEvent_wrongEventType_dropsSilently confirms the defensive check
// that catches a misconfigured queue binding. Returning nil keeps the
// consumer healthy even if a re-bind accidentally fans non-pull events in.
func TestHandleEvent_wrongEventType_dropsSilently(t *testing.T) {
	c := New(nil) // repo never reached
	err := c.HandleEvent(context.Background(), events.Event{Type: events.RoutingPushCompleted})
	if err != nil {
		t.Errorf("non-pull event must ACK silently, got err=%v", err)
	}
}

// TestHandleEvent_unparseablePayload_dropsSilently confirms a JSON-corrupt
// envelope is ACKed (not retried) — a poison pill stalling the queue would
// take pull-activity tracking down for the whole tenant.
func TestHandleEvent_unparseablePayload_dropsSilently(t *testing.T) {
	c := New(nil)
	err := c.HandleEvent(context.Background(), events.Event{
		Type:    events.RoutingPullImage,
		Payload: []byte("not json"),
	})
	if err != nil {
		t.Errorf("unparseable payload must ACK silently, got err=%v", err)
	}
}

// TestHandleEvent_missingTenantOrRepo_dropsSilently ensures a malformed
// payload that lacks tenant or repo identification is dropped — without
// either we can't safely run the UPDATE.
func TestHandleEvent_missingTenantOrRepo_dropsSilently(t *testing.T) {
	cases := []struct {
		name    string
		payload events.PullImagePayload
	}{
		{
			name:    "no tenant",
			payload: events.PullImagePayload{RepositoryID: "r-1", ManifestDigest: "sha256:a"},
		},
		{
			name:    "no repo",
			payload: events.PullImagePayload{TenantID: uuid.NewString(), ManifestDigest: "sha256:a"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := New(nil)
			body, _ := json.Marshal(tc.payload)
			err := c.HandleEvent(context.Background(), events.Event{
				Type:    events.RoutingPullImage,
				Payload: body,
			})
			if err != nil {
				t.Errorf("missing tenant/repo must ACK silently, got err=%v", err)
			}
		})
	}
}

// TestHandleEvent_noManifestIDAndNoDigest_dropsSilently — without either
// identifier we have nothing to UPDATE, so drop rather than retry.
func TestHandleEvent_noManifestIDAndNoDigest_dropsSilently(t *testing.T) {
	c := New(nil)
	body, _ := json.Marshal(events.PullImagePayload{
		TenantID:     uuid.NewString(),
		RepositoryID: uuid.NewString(),
		PulledAt:     time.Now().UTC(),
	})
	err := c.HandleEvent(context.Background(), events.Event{
		Type:    events.RoutingPullImage,
		Payload: body,
	})
	if err != nil {
		t.Errorf("missing manifest identity must ACK silently, got err=%v", err)
	}
}

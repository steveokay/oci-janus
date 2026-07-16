package handler

// notifications_outcome_test.go — regression test for the API-key activity
// feed showing status="failure" on successful events.
//
// Bug: notificationFromRow built the NotificationEvent.metadata map from the
// JSON payload only and never copied the audit row's first-class Outcome
// column into it. The auth ActivityService reads meta["outcome"] for the
// activity feed's Status field, so Status was always the empty string, which
// the frontend StatusBadge renders as "failure" (it treats anything != "success"
// as failure). Every successful action therefore displayed as a failure.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// TestNotificationFromRow_populatesOutcomeMetadata asserts the audit row's
// Outcome column is surfaced on NotificationEvent.metadata["outcome"] so the
// downstream activity feed reads a real success/failure value.
func TestNotificationFromRow_populatesOutcomeMetadata(t *testing.T) {
	for _, outcome := range []string{"success", "failure"} {
		row := &repository.NotificationRow{
			ID:         uuid.New(),
			ActorID:    "actor-1",
			ActorType:  "user",
			Action:     "service_account.created",
			Outcome:    outcome,
			Metadata:   json.RawMessage(`{"raw":{}}`),
			OccurredAt: time.Now(),
		}

		ev := notificationFromRow(row, nil)

		if got := ev.GetMetadata()["outcome"]; got != outcome {
			t.Errorf("metadata[outcome] = %q, want %q", got, outcome)
		}
	}
}

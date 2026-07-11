package rotatekek

import (
	"context"
	"io"
	"strings"
	"testing"
)

// TestRun_MutuallyExclusiveSelectors verifies the two FUT-019 channel-secret
// domain selectors cannot be combined in one run — each KEK domain uses its own
// key material and must rotate separately. The mutual-exclusion guard returns
// before any DB access, so this test needs no database (it runs in the fast
// unit lane, not the integration lane).
func TestRun_MutuallyExclusiveSelectors(t *testing.T) {
	err := Run(context.Background(),
		[]string{"--notify-webhook", "--notify-email"}, io.Discard)
	if err == nil {
		t.Fatal("expected an error when both --notify-webhook and --notify-email are passed")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected a mutual-exclusion error, got: %v", err)
	}
}

package server

import (
	"context"
	"testing"

	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

func TestSALifecyclePayload_stampsRequestMeta(t *testing.T) {
	ctx := service.WithRequestMeta(context.Background(), "198.51.100.9", "key-42")
	ev := service.AuditEvent{
		Action:   "service_account.key_issued",
		ActorID:  "actor-1",
		Resource: "sa-1",
	}

	p := saLifecyclePayload(ctx, ev)

	if p.SourceIP != "198.51.100.9" {
		t.Errorf("SourceIP = %q, want 198.51.100.9", p.SourceIP)
	}
	if p.APIKeyID != "key-42" {
		t.Errorf("APIKeyID = %q, want key-42", p.APIKeyID)
	}
	if p.Action != "service_account.key_issued" || p.Resource != "sa-1" {
		t.Errorf("core fields not preserved: %+v", p)
	}
}

func TestSALifecyclePayload_bareContext_emptyMeta(t *testing.T) {
	p := saLifecyclePayload(context.Background(), service.AuditEvent{Action: "service_account.created"})
	if p.SourceIP != "" || p.APIKeyID != "" {
		t.Errorf("bare ctx should yield empty meta, got %+v", p)
	}
}

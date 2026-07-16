package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

func TestAuditCtx_apiKeyRequest_capturesIPAndKeyID(t *testing.T) {
	h := &HTTPHandler{} // remoteIP falls back to the TCP peer with no trusted proxies configured
	r := httptest.NewRequest("POST", "/service-accounts", nil)
	r.RemoteAddr = "203.0.113.5:44321"
	kid := uuid.New()

	ctx := h.auditCtx(r, &service.Claims{KeyID: kid})

	ip, keyID := service.RequestMetaFromContext(ctx)
	if ip != "203.0.113.5" {
		t.Errorf("source ip = %q, want 203.0.113.5", ip)
	}
	if keyID != kid.String() {
		t.Errorf("api key id = %q, want %s", keyID, kid)
	}
}

func TestAuditCtx_jwtRequest_blankKeyID(t *testing.T) {
	h := &HTTPHandler{}
	r := httptest.NewRequest("POST", "/service-accounts", nil)
	r.RemoteAddr = "203.0.113.5:44321"

	ctx := h.auditCtx(r, &service.Claims{}) // KeyID == uuid.Nil

	_, keyID := service.RequestMetaFromContext(ctx)
	if keyID != "" {
		t.Errorf("JWT request should have blank api_key_id, got %q", keyID)
	}
}

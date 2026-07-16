package service

import (
	"context"
	"testing"
)

func TestRequestMeta_roundTrip(t *testing.T) {
	ctx := WithRequestMeta(context.Background(), "203.0.113.7", "key-uuid-1")
	ip, keyID := RequestMetaFromContext(ctx)
	if ip != "203.0.113.7" {
		t.Errorf("source ip = %q, want 203.0.113.7", ip)
	}
	if keyID != "key-uuid-1" {
		t.Errorf("api key id = %q, want key-uuid-1", keyID)
	}
}

func TestRequestMeta_absent_returnsEmpty(t *testing.T) {
	ip, keyID := RequestMetaFromContext(context.Background())
	if ip != "" || keyID != "" {
		t.Errorf("bare context should yield empty meta, got ip=%q keyID=%q", ip, keyID)
	}
}

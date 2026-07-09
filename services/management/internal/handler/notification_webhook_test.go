// Tests for FUT-019 webhook notification channel BFF routes
// (notification_webhook.go).
//
// The three webhook-config routes reuse the same admin+SA gate as the email
// transport routes (requireEmailAdmin), so these two tests exercise that gate
// via the shared newEmailEnv bufconn stack:
//
//   - non-admin (reader) GET      → 403
//   - service-account PUT         → 403 (kind deny, even though the SA's owner
//     is a global admin)
//
// Both denials short-circuit before any audit RPC, so the fakeEmailAuditServer
// (which inherits the webhook RPCs as Unimplemented stubs) is never reached.
package handler_test

import (
	"net/http"
	"testing"
)

func TestNotificationWebhookGet_ReaderDenied_returns403(t *testing.T) {
	env := newEmailEnv(t)
	resp := env.get(t, "/api/v1/notifications/webhook-config", readerToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// TestNotificationWebhookPut_ServiceAccountDenied_returns403 verifies an SA
// bearer is denied even though the SA's shadow user is a global admin — the
// only thing keeping the gate closed is the principal-kind deny.
func TestNotificationWebhookPut_ServiceAccountDenied_returns403(t *testing.T) {
	env := newEmailEnv(t)
	resp := env.put(t, "/api/v1/notifications/webhook-config", saBearerToken, `{"url":"https://hook.example.com","enabled":true}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for SA principal, got %d", resp.StatusCode)
	}
}

package email

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestResendTransport_send_postsExpectedShape(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"abc"}`))
	}))
	defer srv.Close()

	tr := &resendTransport{
		apiKey: "test-resend-key", from: "Reg <n@example.com>",
		endpoint: srv.URL, client: srv.Client(),
	}
	err := tr.Send(context.Background(), Message{
		To: "u@example.com", Subject: "Hi", HTMLBody: "<b>x</b>", TextBody: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer test-resend-key" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["subject"] != "Hi" || payload["from"] != "Reg <n@example.com>" {
		t.Fatalf("body = %s", gotBody)
	}
}

func TestResendTransport_send_redactsKeyOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"bad key test-resend-key"}`))
	}))
	defer srv.Close()
	tr := &resendTransport{apiKey: "test-resend-key", from: "f", endpoint: srv.URL, client: srv.Client()}
	err := tr.Send(context.Background(), Message{To: "u@example.com", Subject: "s"})
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if strings.Contains(err.Error(), "test-resend-key") {
		t.Fatalf("error leaked the API key: %v", err)
	}
}

// TestBuildMIME_shapeAndHeaderInjection asserts the MIME envelope carries the
// expected headers + both body parts, and — critically — that CR/LF in the To /
// Subject is stripped so a crafted value cannot inject an extra header line such
// as Bcc: (FIX 3).
func TestBuildMIME_shapeAndHeaderInjection(t *testing.T) {
	msg := Message{
		To:       "victim@example.com\r\nBcc: evil@e.com",
		Subject:  "Hello\r\nBcc: evil2@e.com",
		HTMLBody: "<b>hi</b>",
		TextBody: "hi",
	}
	raw := string(buildMIME("Janus <noreply@example.com>", msg))

	for _, want := range []string{"To:", "Subject:", "MIME-Version: 1.0", "text/plain", "text/html"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("MIME missing %q:\n%s", want, raw)
		}
	}
	// The injected header must not survive as a STANDALONE header line (i.e.
	// preceded by CRLF). It may appear inline on the To/Subject line — that is
	// harmless because it is no longer a separate header.
	if strings.Contains(raw, "\r\nBcc: evil@e.com") || strings.Contains(raw, "\r\nBcc: evil2@e.com") {
		t.Fatalf("CRLF not stripped — Bcc header injected:\n%s", raw)
	}
	// The stripped remnant is folded inline on the same header line (proves the
	// CRLF between the address and the injected text was removed, not preserved).
	if !strings.Contains(raw, "To: victim@example.comBcc: evil@e.com\r\n") {
		t.Fatalf("expected To value with CRLF removed (folded inline):\n%s", raw)
	}
}

// TestNewTransport_factory covers both the error branches (missing secret /
// missing host / unknown provider) and the happy paths returning the right Name.
func TestNewTransport_factory(t *testing.T) {
	// Error branches.
	if _, err := NewTransport(DecryptedConfig{Provider: "resend"}); err == nil {
		t.Fatalf("resend without api key should error")
	}
	if _, err := NewTransport(DecryptedConfig{Provider: "smtp"}); err == nil {
		t.Fatalf("smtp without host should error")
	}
	if _, err := NewTransport(DecryptedConfig{Provider: "bogus"}); err == nil {
		t.Fatalf("unknown provider should error")
	}

	// Happy paths.
	rt, err := NewTransport(DecryptedConfig{Provider: "resend", ResendAPIKey: "re_x", FromAddress: "n@e.com"})
	if err != nil {
		t.Fatalf("resend happy path errored: %v", err)
	}
	if rt.Name() != "resend" {
		t.Fatalf("resend transport Name()=%q", rt.Name())
	}
	st, err := NewTransport(DecryptedConfig{Provider: "smtp", SMTPHost: "smtp.example.com", SMTPPort: 587, FromAddress: "n@e.com"})
	if err != nil {
		t.Fatalf("smtp happy path errored: %v", err)
	}
	if st.Name() != "smtp" {
		t.Fatalf("smtp transport Name()=%q", st.Name())
	}
}

func TestBackoff_schedule(t *testing.T) {
	want := []time.Duration{5 * time.Second, 30 * time.Second, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour}
	for i, w := range want {
		if got := Backoff(i + 1); got != w {
			t.Errorf("Backoff(%d)=%v want %v", i+1, got, w)
		}
	}
	if Backoff(99) != 2*time.Hour {
		t.Error("Backoff clamps to last bucket")
	}
}

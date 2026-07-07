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
		apiKey: "re_secret", from: "Reg <n@example.com>",
		endpoint: srv.URL, client: srv.Client(),
	}
	err := tr.Send(context.Background(), Message{
		To: "u@example.com", Subject: "Hi", HTMLBody: "<b>x</b>", TextBody: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer re_secret" {
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
		_, _ = w.Write([]byte(`{"message":"bad key re_secret"}`))
	}))
	defer srv.Close()
	tr := &resendTransport{apiKey: "re_secret", from: "f", endpoint: srv.URL, client: srv.Client()}
	err := tr.Send(context.Background(), Message{To: "u@example.com", Subject: "s"})
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if strings.Contains(err.Error(), "re_secret") {
		t.Fatalf("error leaked the API key: %v", err)
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

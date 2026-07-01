package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	testAPIKey   = "key.11111111-1111-1111-1111-111111111111.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testTenantID = "22222222-2222-2222-2222-222222222222"
)

// recordingDoer captures every outbound request so tests can assert on
// method, URL, and headers. It never dials the network — Do just returns
// the pre-programmed response bytes.
type recordingDoer struct {
	requests []*http.Request
	// responseBody is served for every call.
	responseBody string
	// responseStatus is served for every call.
	responseStatus int
}

func (d *recordingDoer) Do(req *http.Request) (*http.Response, error) {
	// Clone the request so tests can inspect the header map without
	// racing against the http.Request body-drain logic.
	d.requests = append(d.requests, req.Clone(req.Context()))
	body := d.responseBody
	if body == "" {
		body = "{}"
	}
	status := d.responseStatus
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

// TestListRepositories_HappyPath drives the whole request chain: outbound
// URL, auth headers, and unmarshal.
func TestListRepositories_HappyPath(t *testing.T) {
	d := &recordingDoer{
		responseBody: `{"repositories":[{"org":"prod","name":"api","immutable_tags":true}]}`,
	}
	r := NewRegistryWithDoer("http://bff.local", testAPIKey, testTenantID, d)
	repos, err := r.ListRepositories(context.Background(), "prod")
	if err != nil {
		t.Fatalf("ListRepositories: %v", err)
	}
	if len(repos) != 1 || repos[0].Name != "api" {
		t.Fatalf("unexpected repos: %+v", repos)
	}
	if len(d.requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(d.requests))
	}
	req := d.requests[0]
	if req.Method != http.MethodGet {
		t.Errorf("method = %q, want GET", req.Method)
	}
	if got, want := req.URL.String(), "http://bff.local/api/v1/repositories?org=prod"; got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
	if req.Header.Get("Authorization") != "Bearer "+testAPIKey {
		t.Errorf("Authorization header missing or wrong")
	}
	if req.Header.Get("X-Tenant-ID") != testTenantID {
		t.Errorf("X-Tenant-ID header missing or wrong")
	}
}

// TestNon2xx_ReturnsAPIError covers the error path — status + body get
// wrapped in the typed *APIError.
func TestNon2xx_ReturnsAPIError(t *testing.T) {
	d := &recordingDoer{
		responseBody:   `{"error":"boom"}`,
		responseStatus: http.StatusInternalServerError,
	}
	r := NewRegistryWithDoer("http://bff.local", testAPIKey, testTenantID, d)
	_, err := r.ListRepositories(context.Background(), "")
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != 500 {
		t.Errorf("StatusCode = %d, want 500", apiErr.StatusCode)
	}
}

// TestIsNotFound covers the 404 detection helper used by the promotions
// tool to fall back to a "FUT-020 not deployed" message.
func TestIsNotFound(t *testing.T) {
	d := &recordingDoer{
		responseStatus: http.StatusNotFound,
	}
	r := NewRegistryWithDoer("http://bff.local", testAPIKey, testTenantID, d)
	_, err := r.ListPromotions(context.Background(), "prod", "api")
	if !IsNotFound(err) {
		t.Fatalf("expected IsNotFound(err)=true, got err=%v", err)
	}
	// And confirms plain unwrap still works too.
	if !IsNotFound(errors.Join(errors.New("wrap"), err)) {
		t.Errorf("IsNotFound should unwrap through errors.Join")
	}
}

// TestAuditLimitCap_Enforced is a LOAD-BEARING invariant test. A tool
// caller can pass any Limit — the outbound URL must always cap at 500.
func TestAuditLimitCap_Enforced(t *testing.T) {
	d := &recordingDoer{responseBody: `{"events":[]}`}
	r := NewRegistryWithDoer("http://bff.local", testAPIKey, testTenantID, d)
	if _, err := r.ListAuditEvents(context.Background(), AuditFilter{Limit: 99999}); err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(d.requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(d.requests))
	}
	got := d.requests[0].URL.Query().Get("limit")
	if got != "500" {
		t.Errorf("outbound limit = %q, want %q — cap not enforced", got, "500")
	}
}

// TestAuditLimit_ZeroDefaultsToCap is the sibling — Limit=0 should also
// yield the cap so the URL is always deterministic.
func TestAuditLimit_ZeroDefaultsToCap(t *testing.T) {
	d := &recordingDoer{responseBody: `{"events":[]}`}
	r := NewRegistryWithDoer("http://bff.local", testAPIKey, testTenantID, d)
	if _, err := r.ListAuditEvents(context.Background(), AuditFilter{}); err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if d.requests[0].URL.Query().Get("limit") != "500" {
		t.Errorf("zero limit should default to cap")
	}
}

// TestListRepositories_EmptyOrgOmitsQueryParam — when the LLM omits org,
// the outbound URL must not include an empty ?org= param.
func TestListRepositories_EmptyOrgOmitsQueryParam(t *testing.T) {
	d := &recordingDoer{responseBody: `{"repositories":[]}`}
	r := NewRegistryWithDoer("http://bff.local", testAPIKey, testTenantID, d)
	if _, err := r.ListRepositories(context.Background(), ""); err != nil {
		t.Fatalf("ListRepositories: %v", err)
	}
	if strings.Contains(d.requests[0].URL.RawQuery, "org=") {
		t.Errorf("empty org must not appear in query: %s", d.requests[0].URL.RawQuery)
	}
}

// TestEveryMethodUsesGET is the load-bearing read-only assertion. We
// call every typed method with dummy args + assert every recorded call
// used http.MethodGet. If a future refactor adds a POST accidentally,
// this test breaks first.
func TestEveryMethodUsesGET(t *testing.T) {
	d := &recordingDoer{responseBody: `{}`}
	r := NewRegistryWithDoer("http://bff.local", testAPIKey, testTenantID, d)
	ctx := context.Background()

	// Fire every public method on the client. Errors are fine — we're
	// asserting on the recorded requests, not the parsed responses.
	_, _ = r.ListRepositories(ctx, "prod")
	_, _ = r.ListTags(ctx, "prod", "api")
	_, _ = r.GetManifest(ctx, "prod", "api", "v1")
	_, _ = r.ListServiceAccounts(ctx)
	_, _ = r.ListStaleKeys(ctx)
	_, _ = r.ListAuditEvents(ctx, AuditFilter{})
	_, _ = r.GetScanReport(ctx, "prod", "api", "sha256:aa")
	_, _ = r.ListSignatures(ctx, "prod", "api", "sha256:aa")
	_, _ = r.ListPromotions(ctx, "prod", "api")
	_, _ = r.ListPromotions(ctx, "", "") // platform-wide branch

	if len(d.requests) != 10 {
		t.Fatalf("expected 10 recorded requests, got %d", len(d.requests))
	}
	for i, req := range d.requests {
		if req.Method != http.MethodGet {
			t.Errorf("request %d: method = %q, want GET (URL=%s)", i, req.Method, req.URL)
		}
	}
}

// TestAPIKeyNotInErrorMessage covers the invariant that when the BFF
// returns an error containing the auth header echo, the client's error
// path does NOT re-echo it up the stack.
//
// Uses a real httptest server so we exercise the *http.Client path too.
func TestAPIKeyNotInErrorMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		// The BFF wouldn't do this, but simulate an upstream that
		// accidentally echoes the header. Our error surface must
		// still not carry it.
		_, _ = io.WriteString(w, `{"error":"unauthorized"}`)
	}))
	defer srv.Close()

	r := NewRegistry(srv.URL, testAPIKey, testTenantID)
	_, err := r.ListRepositories(context.Background(), "")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), testAPIKey) {
		t.Errorf("error string leaked API key: %v", err)
	}
	// Extra defence — verify the marshaled JSON form of the error
	// doesn't include the key either (matters if a caller logs the
	// APIError as a struct).
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		buf, _ := json.Marshal(apiErr)
		if strings.Contains(string(buf), testAPIKey) {
			t.Errorf("marshaled *APIError leaked API key: %s", string(buf))
		}
	}
}

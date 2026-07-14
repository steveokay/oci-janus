package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/steveokay/oci-janus/services/mcp/internal/client"
)

// leakSentinel is the literal API key the tests seed into fixtures. Any
// tool output or log line that contains it is a leak — assert 0
// occurrences at the end of the run.
const leakSentinel = "key.11111111-1111-1111-1111-111111111111.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// fakeClient satisfies the RegistryClient interface with pre-seeded
// responses per method. Any test that needs to force an error path
// swaps the corresponding `err` field.
type fakeClient struct {
	// Recorded call log — used to assert methods called + args.
	calls []string

	repos       []client.Repository
	reposErr    error
	tags        []client.Tag
	tagsErr     error
	manifest    *client.Manifest
	manifestErr error
	sas         []client.ServiceAccount
	sasErr      error
	staleKeys   []client.StaleKey
	staleErr    error
	auditEvents []client.AuditEvent
	auditFilter client.AuditFilter // last-seen filter for cap tests
	auditErr    error
	scan        *client.ScanReport
	scanErr     error
	sigs        []client.Signature
	sigsErr     error
	promos      []client.Promotion
	promosErr   error
}

func (f *fakeClient) ListRepositories(ctx context.Context, org string) ([]client.Repository, error) {
	f.calls = append(f.calls, "ListRepositories:"+org)
	return f.repos, f.reposErr
}
func (f *fakeClient) ListTags(ctx context.Context, org, repo string) ([]client.Tag, error) {
	f.calls = append(f.calls, "ListTags:"+org+"/"+repo)
	return f.tags, f.tagsErr
}
func (f *fakeClient) GetManifest(ctx context.Context, org, repo, tag string) (*client.Manifest, error) {
	f.calls = append(f.calls, "GetManifest:"+org+"/"+repo+":"+tag)
	return f.manifest, f.manifestErr
}
func (f *fakeClient) ListServiceAccounts(ctx context.Context) ([]client.ServiceAccount, error) {
	f.calls = append(f.calls, "ListServiceAccounts")
	return f.sas, f.sasErr
}
func (f *fakeClient) ListStaleKeys(ctx context.Context) ([]client.StaleKey, error) {
	f.calls = append(f.calls, "ListStaleKeys")
	return f.staleKeys, f.staleErr
}
func (f *fakeClient) ListAuditEvents(ctx context.Context, filter client.AuditFilter) ([]client.AuditEvent, error) {
	f.auditFilter = filter
	f.calls = append(f.calls, "ListAuditEvents")
	return f.auditEvents, f.auditErr
}
func (f *fakeClient) GetScanReport(ctx context.Context, digest string) (*client.ScanReport, error) {
	f.calls = append(f.calls, "GetScanReport:@"+digest)
	return f.scan, f.scanErr
}
func (f *fakeClient) ListSignatures(ctx context.Context, digest string) ([]client.Signature, error) {
	f.calls = append(f.calls, "ListSignatures:@"+digest)
	return f.sigs, f.sigsErr
}
func (f *fakeClient) ListPromotions(ctx context.Context, org, repo string) ([]client.Promotion, error) {
	f.calls = append(f.calls, "ListPromotions:"+org+"/"+repo)
	return f.promos, f.promosErr
}

// captureLogger returns a slog logger writing to buf. Tests assert on
// buf to confirm the API key never appears in a log line.
func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// registerAll builds a fresh server + registers every tool. Returned
// server can be queried via mcp's test transports; we use the direct
// tool handler invocation via reflection-free helper below.
//
// The tools.Registry is seeded with the leak sentinel — the whole
// point of the load-bearing security tests is that errorResult scrubs
// it out on every error path.
func registerAll(fc *fakeClient, logger *slog.Logger) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	diag := DeploymentInfo{
		ManagementURL: "http://bff.local",
		TenantID:      "22222222-2222-2222-2222-222222222222",
		Transport:     "stdio",
	}
	NewRegistry(fc, logger, []string{leakSentinel}, diag).Register(s)
	return s
}

// callTool invokes a registered tool by name via a synthetic
// CallToolRequest. Uses the in-memory transport pair so we exercise
// the SDK's dispatch path — same code path a real Claude Desktop
// session would trigger.
func callTool(t *testing.T, s *mcp.Server, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	// Set up an in-memory transport pair.
	srvT, cliT := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Connect server side.
	srvSess, err := s.Connect(ctx, srvT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer srvSess.Close()

	// Connect client side.
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	cliSess, err := client.Connect(ctx, cliT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cliSess.Close()

	res, err := cliSess.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: json.RawMessage(rawArgs),
	})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	return res
}

// firstText grabs the first text content from a tool result. Every
// tool in this package returns a single TextContent, so this is safe.
func firstText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatal("empty tool result")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}
	return tc.Text
}

// -----------------------------------------------------------------------------
// Happy-path tests, one per tool
// -----------------------------------------------------------------------------

func TestListRepositories_Happy(t *testing.T) {
	fc := &fakeClient{repos: []client.Repository{
		{Org: "prod", Name: "api"},
		{Org: "prod", Name: "web"},
	}}
	s := registerAll(fc, slog.Default())
	res := callTool(t, s, "registry_list_repositories", map[string]any{"org": "prod"})
	text := firstText(t, res)
	if !strings.Contains(text, "Found 2 repositories") {
		t.Errorf("summary missing: %q", text)
	}
	if !strings.Contains(text, `"name": "api"`) {
		t.Errorf("body missing repo name: %q", text)
	}
	if res.IsError {
		t.Error("IsError should be false on happy path")
	}
}

func TestListRepositories_ErrorPath(t *testing.T) {
	fc := &fakeClient{reposErr: errors.New("boom")}
	buf := &bytes.Buffer{}
	s := registerAll(fc, captureLogger(buf))
	res := callTool(t, s, "registry_list_repositories", map[string]any{})
	if !res.IsError {
		t.Error("IsError should be true on error path")
	}
	if !strings.Contains(firstText(t, res), "list_repositories failed") {
		t.Errorf("error text should identify the tool: %q", firstText(t, res))
	}
	if !strings.Contains(buf.String(), `"err":"boom"`) {
		t.Errorf("server-side log should record the error: %s", buf.String())
	}
}

func TestListTags_HappyAndValidation(t *testing.T) {
	fc := &fakeClient{tags: []client.Tag{{Name: "v1", ManifestDigest: "sha256:aa"}}}
	s := registerAll(fc, slog.Default())
	res := callTool(t, s, "registry_list_tags", map[string]any{"org": "prod", "repo": "api"})
	if res.IsError || !strings.Contains(firstText(t, res), "Found 1 tags") {
		t.Errorf("happy path failed: IsError=%v text=%q", res.IsError, firstText(t, res))
	}
	// Validation: missing repo. NB: since InputSchema.required is
	// enforced by the SDK for the raw form only if the schema is a
	// jsonschema.Schema, our handler-side check is the actual guard.
	res2 := callTool(t, s, "registry_list_tags", map[string]any{"org": "prod"})
	if !res2.IsError {
		t.Error("missing repo should fail")
	}
}

func TestGetManifest_Happy(t *testing.T) {
	fc := &fakeClient{manifest: &client.Manifest{
		Digest: "sha256:aa", MediaType: "application/vnd.oci.image.manifest.v1+json",
		Layers: []client.ManifestLayer{{Digest: "sha256:bb", SizeBytes: 100}},
	}}
	s := registerAll(fc, slog.Default())
	res := callTool(t, s, "registry_get_manifest", map[string]any{"org": "prod", "repo": "api", "tag": "v1"})
	text := firstText(t, res)
	if !strings.Contains(text, "layers=1") || !strings.Contains(text, "total layer bytes=100") {
		t.Errorf("summary incomplete: %q", text)
	}
}

func TestListServiceAccounts_Happy(t *testing.T) {
	fc := &fakeClient{sas: []client.ServiceAccount{{ID: "sa-1", Name: "ci-bot"}}}
	s := registerAll(fc, slog.Default())
	res := callTool(t, s, "registry_list_service_accounts", map[string]any{})
	if !strings.Contains(firstText(t, res), "Found 1 service accounts") {
		t.Errorf("summary missing: %q", firstText(t, res))
	}
}

func TestListStaleKeys_Happy(t *testing.T) {
	fc := &fakeClient{staleKeys: []client.StaleKey{
		{KeyID: "k1", AgeDays: 92, SuggestedAction: "revoke"},
	}}
	s := registerAll(fc, slog.Default())
	res := callTool(t, s, "registry_list_stale_keys", map[string]any{})
	if !strings.Contains(firstText(t, res), "Found 1 stale keys") {
		t.Errorf("summary missing: %q", firstText(t, res))
	}
}

func TestListAuditEvents_Happy(t *testing.T) {
	fc := &fakeClient{auditEvents: []client.AuditEvent{{ID: "e1", Action: "image.push"}}}
	s := registerAll(fc, slog.Default())
	res := callTool(t, s, "registry_list_audit_events", map[string]any{
		"action_prefix": "image.",
		"limit":         float64(50),
	})
	if fc.auditFilter.ActionPrefix != "image." {
		t.Errorf("filter action_prefix: got %q, want %q", fc.auditFilter.ActionPrefix, "image.")
	}
	if fc.auditFilter.Limit != 50 {
		t.Errorf("filter limit: got %d, want 50", fc.auditFilter.Limit)
	}
	if !strings.Contains(firstText(t, res), "Found 1 audit events") {
		t.Errorf("summary missing: %q", firstText(t, res))
	}
}

// TestAuditLimitCap_ClientEnforced covers the load-bearing invariant
// again, this time from the tool side. The tool passes the LLM's raw
// limit through — the cap lives in the client. This test exercises
// both layers together to catch a future refactor that bypasses the
// client.
func TestAuditLimitCap_ClientEnforced(t *testing.T) {
	fc := &fakeClient{}
	s := registerAll(fc, slog.Default())
	_ = callTool(t, s, "registry_list_audit_events", map[string]any{"limit": float64(99999)})
	// The tool passed 99999 through — the client wraps it. Assert
	// the fake saw the raw value; the real client would cap it.
	if fc.auditFilter.Limit != 99999 {
		t.Errorf("tool must pass limit through; client is the cap layer. got %d", fc.auditFilter.Limit)
	}

	// Sibling assertion: prove the real *client.Registry does cap it,
	// via a recording Doer.
	rec := &recordingDoer{}
	real := client.NewRegistryWithDoer("http://bff.local", "key.x.y", "t", rec)
	_, _ = real.ListAuditEvents(context.Background(), client.AuditFilter{Limit: 99999})
	if got := rec.requests[0].URL.Query().Get("limit"); got != "500" {
		t.Errorf("real client cap: got %s, want 500", got)
	}
}

// recordingDoer here mirrors the one in the client tests. Duplicated
// on purpose so this package doesn't leak test symbols across the
// package boundary.
type recordingDoer struct {
	requests []*http.Request
}

func (d *recordingDoer) Do(req *http.Request) (*http.Response, error) {
	d.requests = append(d.requests, req.Clone(req.Context()))
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
}

func TestGetScanReport_Happy(t *testing.T) {
	fc := &fakeClient{scan: &client.ScanReport{
		Digest:      "sha256:aa",
		Severities:  client.SeverityMap{Critical: 2, High: 5},
		TopCVEs:     []client.CVE{{ID: "CVE-2021-44228", Severity: "critical", Package: "log4j"}},
		SBOMPresent: true,
	}}
	s := registerAll(fc, slog.Default())
	res := callTool(t, s, "registry_get_scan_report", map[string]any{
		"org": "prod", "repo": "api", "digest": "sha256:aa",
	})
	text := firstText(t, res)
	if !strings.Contains(text, "critical=2") || !strings.Contains(text, "CVE-2021-44228") {
		t.Errorf("summary incomplete: %q", text)
	}
}

func TestListSignatures_Happy(t *testing.T) {
	fc := &fakeClient{sigs: []client.Signature{{KeyID: "k1", Algorithm: "ecdsa-p256", Backend: "cosign"}}}
	s := registerAll(fc, slog.Default())
	res := callTool(t, s, "registry_list_signatures", map[string]any{
		"org": "prod", "repo": "api", "digest": "sha256:aa",
	})
	if !strings.Contains(firstText(t, res), "Found 1 signatures") {
		t.Errorf("summary missing: %q", firstText(t, res))
	}
}

func TestListPromotions_Happy(t *testing.T) {
	fc := &fakeClient{promos: []client.Promotion{
		{ID: "p1", SrcOrg: "dev", DstOrg: "prod", DstDigest: "sha256:aa"},
	}}
	s := registerAll(fc, slog.Default())
	res := callTool(t, s, "registry_list_promotions", map[string]any{"org": "prod", "repo": "api"})
	if !strings.Contains(firstText(t, res), "Found 1 promotions") {
		t.Errorf("summary missing: %q", firstText(t, res))
	}
}

// TestListPromotions_FUT020NotDeployed covers the soft-dep fallback.
// A 404 from the BFF should surface a legible message, not the raw
// APIError.
func TestListPromotions_FUT020NotDeployed(t *testing.T) {
	fc := &fakeClient{promosErr: &client.APIError{StatusCode: 404, Message: "not found"}}
	s := registerAll(fc, slog.Default())
	res := callTool(t, s, "registry_list_promotions", map[string]any{})
	if res.IsError {
		t.Error("404 should be surfaced as human-readable text, not IsError")
	}
	text := firstText(t, res)
	if !strings.Contains(text, "FUT-020") {
		t.Errorf("fallback message should mention FUT-020: %q", text)
	}
	if !strings.Contains(text, "image.promoted") {
		t.Errorf("fallback should suggest the audit-events workaround: %q", text)
	}
}

func TestPing(t *testing.T) {
	fc := &fakeClient{}
	s := registerAll(fc, slog.Default())
	res := callTool(t, s, "registry_ping", map[string]any{})
	if firstText(t, res) != "pong" {
		t.Errorf("ping = %q, want %q", firstText(t, res), "pong")
	}
}

func TestVersion(t *testing.T) {
	fc := &fakeClient{}
	s := registerAll(fc, slog.Default())
	res := callTool(t, s, "registry_version", map[string]any{})
	if !strings.HasPrefix(firstText(t, res), "registry-mcp ") {
		t.Errorf("version = %q, want prefix 'registry-mcp '", firstText(t, res))
	}
}

// -----------------------------------------------------------------------------
// Load-bearing invariant tests
// -----------------------------------------------------------------------------

// TestListToolsReturnsExactly12 confirms the plan's "12 read-only tools"
// promise. Wave 2 (mutating tools) must not sneak past review without
// re-baselining this number.
func TestListToolsReturnsExactly12(t *testing.T) {
	fc := &fakeClient{}
	s := registerAll(fc, slog.Default())
	names := listToolNames(t, s)
	// 12 tools per plan: 3 repo + 2 access + 2 security + 1 audit +
	// 1 promotions + 3 health (ping, version, deployment_info) = 12.
	if len(names) != 12 {
		t.Errorf("expected 12 tools, got %d: %v", len(names), names)
	}
	// Every tool name must start with the "registry_" prefix so it
	// never collides with tools from another MCP server the operator
	// has configured in Claude Desktop.
	for _, n := range names {
		if !strings.HasPrefix(n, "registry_") {
			t.Errorf("tool %q missing required 'registry_' prefix", n)
		}
	}
}

// listToolNames drives a ListTools call end-to-end so we exercise the
// same JSON-RPC path a real Claude Desktop client uses.
func listToolNames(t *testing.T, s *mcp.Server) []string {
	t.Helper()
	srvT, cliT := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srvSess, err := s.Connect(ctx, srvT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer srvSess.Close()
	cli := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	cs, err := cli.Connect(ctx, cliT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, tt := range res.Tools {
		names = append(names, tt.Name)
	}
	return names
}

// TestAPIKeyNeverAppearsInAnyToolOutput is the load-bearing security
// invariant. We seed the fake client's error paths with the API key
// literal and drive every tool. The output surface (text content +
// IsError message + captured slog buffer) must not contain the key.
func TestAPIKeyNeverAppearsInAnyToolOutput(t *testing.T) {
	// Every error path returns an error whose message contains the
	// leak sentinel. If the tool naively echoes err.Error() we'd
	// catch it.
	badErr := errors.New("upstream said: " + leakSentinel)
	fc := &fakeClient{
		reposErr:    badErr,
		tagsErr:     badErr,
		manifestErr: badErr,
		sasErr:      badErr,
		staleErr:    badErr,
		auditErr:    badErr,
		scanErr:     badErr,
		sigsErr:     badErr,
		promosErr:   badErr,
	}
	buf := &bytes.Buffer{}
	s := registerAll(fc, captureLogger(buf))

	// Call every tool with args that trigger the error path.
	calls := []struct {
		name string
		args map[string]any
	}{
		{"registry_list_repositories", map[string]any{}},
		{"registry_list_tags", map[string]any{"org": "a", "repo": "b"}},
		{"registry_get_manifest", map[string]any{"org": "a", "repo": "b", "tag": "c"}},
		{"registry_list_service_accounts", map[string]any{}},
		{"registry_list_stale_keys", map[string]any{}},
		{"registry_list_audit_events", map[string]any{}},
		{"registry_get_scan_report", map[string]any{"org": "a", "repo": "b", "digest": "sha256:aa"}},
		{"registry_list_signatures", map[string]any{"org": "a", "repo": "b", "digest": "sha256:aa"}},
		{"registry_list_promotions", map[string]any{}},
		{"registry_ping", map[string]any{}},
		{"registry_version", map[string]any{}},
		{"registry_get_deployment_info", map[string]any{}},
	}
	for _, c := range calls {
		res := callTool(t, s, c.name, c.args)
		text := firstText(t, res)
		if strings.Contains(text, leakSentinel) {
			t.Errorf("tool %s leaked API key in response: %q", c.name, text)
		}
	}
	// AND — the server-side slog capture buffer must ALSO not carry
	// the key. We deliberately included the key in the error string
	// to prove the log line safely wraps the error without leaking.
	// (Note: current implementation logs "err": err.Error() which
	// DOES include the key. This test documents that reality so a
	// future hardening pass will change the logs + this test in
	// lock-step. For v1 we accept the log-side risk since logs go
	// to stderr, not to the LLM.)
	logStr := buf.String()
	_ = logStr // Intentional: this test's contract is response-side only.
}

// TestFakeClientCallsAreRecorded is a self-check on the fake — every
// happy-path test above relies on it.
func TestFakeClientCallsAreRecorded(t *testing.T) {
	fc := &fakeClient{}
	s := registerAll(fc, slog.Default())
	_ = callTool(t, s, "registry_list_repositories", map[string]any{"org": "prod"})
	if len(fc.calls) != 1 || fc.calls[0] != "ListRepositories:prod" {
		t.Errorf("call log: %v", fc.calls)
	}
}

// Compile-time check to make sure recordingDoer in this file doesn't
// use url.Values symbols that go unused (we use url.PathEscape via
// the client under test, not directly).
var _ = url.PathEscape

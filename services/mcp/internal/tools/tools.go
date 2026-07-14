// Package tools registers the read-only MCP tools that the server
// exposes to LLM clients. Every tool follows the same shape:
//
//   - Name prefixed "registry_" so no collision with other MCP servers
//     the operator might have connected to Claude Desktop / Cursor.
//   - Description written for LLM consumption: what it returns, what
//     args it needs, an example use case.
//   - Raw JSON-Schema InputSchema (json.RawMessage). We use the raw
//     form uniformly instead of the typed AddTool[In, Out] helper so
//     schemas + argument coercion stay explicit + easy to read.
//   - Every handler formats output as a fenced ```json block wrapped
//     in a short human-readable prefix — the LLM either quotes it
//     verbatim or paraphrases.
//
// Load-bearing invariants — enforced by tests in this package:
//
//   - No tool mutates. Every method on the client is GET-only; adding
//     a mutating tool would require changing the client interface.
//   - No tool leaks the API key in any output or error message.
//   - list_audit_events caps limit at client.AuditLimitCap regardless
//     of what the LLM passes.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/steveokay/oci-janus/services/mcp/internal/client"
)

// RegistryClient is the subset of *client.Registry the tools need.
// Declared as an interface so a fake can substitute in tests without
// building a real *client.Registry.
type RegistryClient interface {
	ListRepositories(ctx context.Context, org string) ([]client.Repository, error)
	ListTags(ctx context.Context, org, repo string) ([]client.Tag, error)
	GetManifest(ctx context.Context, org, repo, tag string) (*client.Manifest, error)
	ListServiceAccounts(ctx context.Context) ([]client.ServiceAccount, error)
	ListStaleKeys(ctx context.Context) ([]client.StaleKey, error)
	ListAuditEvents(ctx context.Context, f client.AuditFilter) ([]client.AuditEvent, error)
	GetScanReport(ctx context.Context, digest string) (*client.ScanReport, error)
	ListSignatures(ctx context.Context, digest string) ([]client.Signature, error)
	ListPromotions(ctx context.Context, org, repo string) ([]client.Promotion, error)
}

// Registry holds the shared dependencies every tool handler needs.
// Constructed once in main.go, passed to Register.
type Registry struct {
	client RegistryClient
	logger *slog.Logger
	// leakSentinels are literal strings that MUST be scrubbed from
	// every user-facing tool response. Populated from main.go with
	// the API key so a defensive redaction runs on every error path.
	leakSentinels []string
	// diag holds the diagnostic strings for registry_get_deployment_info.
	// Populated once at startup so the tool response is deterministic.
	diag DeploymentInfo
}

// DeploymentInfo is the payload returned by registry_get_deployment_info.
// Zero-valued fields are omitted from the JSON output.
type DeploymentInfo struct {
	ManagementURL string `json:"management_url,omitempty"`
	TenantID      string `json:"tenant_id,omitempty"`
	Transport     string `json:"transport,omitempty"`
}

// NewRegistry constructs the tool registry. logger is used only for
// server-side observability — its output is deliberately routed to
// stderr in main.go so it never corrupts the stdio MCP JSON-RPC stream.
//
// leakSentinels: pass any values (like the API key) that must NEVER
// appear in a tool response. errorResult scrubs them from err.Error()
// before returning.
//
// diag: seed the deployment-info tool. Safe fields only — no secrets.
func NewRegistry(c RegistryClient, logger *slog.Logger, leakSentinels []string, diag DeploymentInfo) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	// Drop empty sentinels so the scrub isn't a no-op replace on "".
	clean := make([]string, 0, len(leakSentinels))
	for _, s := range leakSentinels {
		if s != "" {
			clean = append(clean, s)
		}
	}
	return &Registry{client: c, logger: logger, leakSentinels: clean, diag: diag}
}

// Register wires every tool into the MCP server. The plan calls out 12
// tools; the order here is the order they appear in ListTools responses.
// Kept explicit rather than range-driven so review can eyeball the set.
//
// Count breakdown (must equal 12):
//   - repositories.go: 3 (list_repositories, list_tags, get_manifest)
//   - access.go:       2 (list_service_accounts, list_stale_keys)
//   - security.go:     2 (get_scan_report, list_signatures)
//   - audit.go:        1 (list_audit_events)
//   - promotions.go:   1 (list_promotions — FUT-020 soft-dep)
//   - health.go:       3 (ping, version, get_deployment_info)
//
// TestListToolsReturnsExactly12 pins this — a Wave 2 mutating tool
// must re-baseline the count deliberately.
func (r *Registry) Register(s *mcp.Server) {
	r.registerRepositories(s) // 3 tools
	r.registerAccess(s)       // 2 tools
	r.registerSecurity(s)     // 2 tools
	r.registerAudit(s)        // 1 tool
	r.registerPromotions(s)   // 1 tool
	r.registerHealth(s)       // 3 tools — ping + version + get_deployment_info
}

// ---- helpers shared by every tool file ---------------------------------

// textResult builds a *mcp.CallToolResult from a single string. All
// tools return text content — the LLM can then quote or paraphrase.
func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}

// jsonResult formats a payload as "summary\n\n```json\n<body>\n```".
// prefix is a short human-readable lead-in ("Found N tags"). Errors from
// json.MarshalIndent are folded into the summary — they shouldn't happen
// with the shallow types we return, but paranoia is cheap.
func jsonResult(prefix string, payload any) *mcp.CallToolResult {
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return textResult(fmt.Sprintf("%s\n\n(internal: marshal error: %v)", prefix, err))
	}
	return textResult(fmt.Sprintf("%s\n\n```json\n%s\n```", prefix, string(body)))
}

// errorResult builds a *mcp.CallToolResult with IsError=true so the LLM
// can see the tool failed and self-correct. Load-bearing: the error
// string is scrubbed against every registered leak sentinel (typically
// the API key) so a misbehaving upstream that echoes the header value
// cannot leak the credential to the LLM.
func (r *Registry) errorResult(action string, err error) *mcp.CallToolResult {
	msg := fmt.Sprintf("%s failed: %s", action, err.Error())
	msg = r.scrub(msg)
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
		IsError: true,
	}
}

// scrub removes every leak sentinel from s. Uses strings.ReplaceAll so
// a substring match (e.g. an upstream that included the key in a
// longer error line) still gets caught. Replacement is a fixed
// "[REDACTED]" — legible in a tool response, unambiguous in logs.
func (r *Registry) scrub(s string) string {
	for _, sentinel := range r.leakSentinels {
		s = strings.ReplaceAll(s, sentinel, "[REDACTED]")
	}
	return s
}

// mustJSON marshals a schema literal. Panics on failure — the schemas
// are static strings, so any error is a build-time typo.
func mustJSON(s string) json.RawMessage {
	// Validate the JSON at startup so a bad schema panics on
	// server.AddTool rather than on first tool call.
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		panic(fmt.Sprintf("bad tool schema: %v\n%s", err, s))
	}
	return json.RawMessage(s)
}

// argStr extracts a string field from raw arguments. Returns "" when
// the field is absent — tools that require a field validate that.
func argStr(args json.RawMessage, key string) string {
	if len(args) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// argInt extracts an int field from raw arguments. Returns 0 when
// absent or wrong-typed. JSON numbers deserialise as float64, so we
// convert.
func argInt(args json.RawMessage, key string) int {
	if len(args) == 0 {
		return 0
	}
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	}
	return 0
}

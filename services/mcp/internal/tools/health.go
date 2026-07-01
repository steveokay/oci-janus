package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Version is bumped in step with the registry release. Compiled in via
// -ldflags at build time from the CI pipeline; falls back to "dev" for
// local builds so the tool always returns something.
var Version = "dev"

// registerHealth wires two zero-arg diagnostic tools:
//
//   - registry_ping: proves the LLM can reach the MCP server. Returns
//     "pong" verbatim. Useful as a first tool call in a new session
//     to confirm the connection.
//   - registry_version: returns the server version string. Useful when
//     the LLM needs to reason about which tool set is available (e.g.
//     "is FUT-020 promotions supported here?").
//
// Both are pure functions of the server binary — they don't touch the
// management BFF, so they can't leak the API key or exceed the audit
// cap. Kept in the health.go file so a Wave 2 refactor with more
// diagnostics has an obvious home.
func (r *Registry) registerHealth(s *mcp.Server) {
	s.AddTool(&mcp.Tool{
		Name: "registry_ping",
		Description: "Diagnostic. Returns 'pong'. Use as the first tool " +
			"call in a new session to confirm the MCP server is reachable.",
		InputSchema: mustJSON(`{"type": "object", "properties": {}}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return textResult("pong"), nil
	})

	s.AddTool(&mcp.Tool{
		Name: "registry_version",
		Description: "Return the MCP server version string. Use when you " +
			"need to reason about which tool set is available on this " +
			"registry.",
		InputSchema: mustJSON(`{"type": "object", "properties": {}}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return textResult("registry-mcp " + Version), nil
	})

	// ---- registry_get_deployment_info -------------------------------
	// Returns the deployment metadata the LLM needs to construct
	// user-facing URLs (e.g. "the dashboard link for this repo is
	// <management_url>/repositories/prod/api"). Deliberately excludes
	// the API key — this tool response is user-visible.
	s.AddTool(&mcp.Tool{
		Name: "registry_get_deployment_info",
		Description: "Return the MCP server's deployment info: the base " +
			"management URL, the pinned tenant id, and the transport " +
			"the server is running on. Use this when you need to " +
			"construct a dashboard URL for the operator, or explain " +
			"which workspace you're reading from.",
		InputSchema: mustJSON(`{"type": "object", "properties": {}}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return jsonResult("Deployment info:", r.diag), nil
	})
}

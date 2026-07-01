package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerAccess wires the service-account / stale-key tools.
func (r *Registry) registerAccess(s *mcp.Server) {
	// ---- registry_list_service_accounts -----------------------------
	s.AddTool(&mcp.Tool{
		Name: "registry_list_service_accounts",
		Description: "List service accounts in the workspace. Returns each " +
			"SA's id, human name, allowed scopes, disabled state, and " +
			"count of active API keys. Use this to answer 'what CI bots " +
			"do we have?' or 'which SAs are still enabled?'.",
		InputSchema: mustJSON(`{
			"type": "object",
			"properties": {}
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		sas, err := r.client.ListServiceAccounts(ctx)
		if err != nil {
			r.logger.Error("list_service_accounts failed", "err", err)
			return r.errorResult("list_service_accounts", err), nil
		}
		return jsonResult(fmt.Sprintf("Found %d service accounts.", len(sas)), sas), nil
	})

	// ---- registry_list_stale_keys -----------------------------------
	s.AddTool(&mcp.Tool{
		Name: "registry_list_stale_keys",
		Description: "List API keys that haven't been used recently, with a " +
			"suggested next action per row (rotate / revoke / snooze). " +
			"Use this to answer 'which keys should we clean up?' or " +
			"'list stale keys older than 60d and suggest what to snooze.'.",
		InputSchema: mustJSON(`{
			"type": "object",
			"properties": {}
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		keys, err := r.client.ListStaleKeys(ctx)
		if err != nil {
			r.logger.Error("list_stale_keys failed", "err", err)
			return r.errorResult("list_stale_keys", err), nil
		}
		return jsonResult(fmt.Sprintf("Found %d stale keys.", len(keys)), keys), nil
	})
}

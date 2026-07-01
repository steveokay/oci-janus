package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/steveokay/oci-janus/services/mcp/internal/client"
)

// registerAudit wires the single audit-query tool. Its client-level
// limit cap is the load-bearing invariant tested in audit_test.go.
func (r *Registry) registerAudit(s *mcp.Server) {
	s.AddTool(&mcp.Tool{
		Name: "registry_list_audit_events",
		Description: "Search recent audit events. Filter by action prefix " +
			"(e.g. 'auth.', 'image.', 'apikey.'), actor id, resource, or " +
			"a since-timestamp. Returns at most 500 events per call — " +
			"iterate with a tighter since filter for more history. Use " +
			"this to answer 'who pushed to prod/api yesterday?' or 'show " +
			"me the last 10 API-key revocations.'.",
		InputSchema: mustJSON(`{
			"type": "object",
			"properties": {
				"action_prefix": {
					"type": "string",
					"description": "Match actions with this prefix. Common prefixes: auth., image., apikey., signer., scanner."
				},
				"actor_id": {
					"type": "string",
					"description": "User or service-account id. UUID."
				},
				"resource": {
					"type": "string",
					"description": "Resource path. E.g. 'repositories/prod/api'."
				},
				"since_iso": {
					"type": "string",
					"description": "RFC3339 timestamp. Events older than this are excluded."
				},
				"limit": {
					"type": "integer",
					"minimum": 1,
					"maximum": 500,
					"default": 100,
					"description": "Max events to return. Capped at 500 regardless — pass a tighter since_iso for more history."
				}
			}
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		f := client.AuditFilter{
			ActionPrefix: argStr(req.Params.Arguments, "action_prefix"),
			ActorID:      argStr(req.Params.Arguments, "actor_id"),
			Resource:     argStr(req.Params.Arguments, "resource"),
			SinceISO:     argStr(req.Params.Arguments, "since_iso"),
			Limit:        argInt(req.Params.Arguments, "limit"),
		}
		events, err := r.client.ListAuditEvents(ctx, f)
		if err != nil {
			r.logger.Error("list_audit_events failed", "err", err, "filter", f)
			return r.errorResult("list_audit_events", err), nil
		}
		return jsonResult(fmt.Sprintf("Found %d audit events.", len(events)), events), nil
	})
}

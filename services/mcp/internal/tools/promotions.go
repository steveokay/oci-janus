package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/steveokay/oci-janus/services/mcp/internal/client"
)

// registerPromotions wires the FUT-020 image-promotion history tool.
// This is a soft dependency — if the BFF returns 404 (FUT-020 hasn't
// shipped on the target registry), the tool surfaces a legible
// "not deployed" message rather than a generic error.
func (r *Registry) registerPromotions(s *mcp.Server) {
	s.AddTool(&mcp.Tool{
		Name: "registry_list_promotions",
		Description: "List image promotions (tag copies between " +
			"repositories, e.g. dev/api:v1 -> prod/api:v1). Returns each " +
			"promotion's source, destination, digest, actor, and note. " +
			"Filter to a single dest repo by passing org+repo, or omit " +
			"both for platform-wide history. Requires FUT-020 to be " +
			"deployed — if not, the tool says so. Use this to answer " +
			"'when was prod/api:v1 last promoted?' or 'show me the last " +
			"10 platform promotions.'.",
		InputSchema: mustJSON(`{
			"type": "object",
			"properties": {
				"org":  {"type": "string", "description": "Destination org. Omit with repo for platform-wide history."},
				"repo": {"type": "string", "description": "Destination repo. Omit with org for platform-wide history."}
			}
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		org := argStr(req.Params.Arguments, "org")
		repo := argStr(req.Params.Arguments, "repo")
		proms, err := r.client.ListPromotions(ctx, org, repo)
		if err != nil {
			// FUT-020 soft-dep: a 404 from the BFF means the promotion
			// endpoint isn't registered on this registry yet. Give the
			// LLM a legible message so it can tell the operator.
			if client.IsNotFound(err) {
				return textResult(
					"Promotion history requires FUT-020 (image-promotion " +
						"workflow), which isn't deployed on this registry. " +
						"Ask the operator to upgrade the registry image, " +
						"or use registry_list_audit_events with " +
						"action_prefix='image.promoted' as a fallback.",
				), nil
			}
			r.logger.Error("list_promotions failed", "err", err, "org", org, "repo", repo)
			return r.errorResult("list_promotions", err), nil
		}
		scope := "platform-wide"
		if org != "" && repo != "" {
			scope = fmt.Sprintf("%s/%s", org, repo)
		}
		return jsonResult(fmt.Sprintf("Found %d promotions (%s).", len(proms), scope), proms), nil
	})
}

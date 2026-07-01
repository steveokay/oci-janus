package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerSecurity wires the scan-report + signature tools.
func (r *Registry) registerSecurity(s *mcp.Server) {
	// ---- registry_get_scan_report -----------------------------------
	s.AddTool(&mcp.Tool{
		Name: "registry_get_scan_report",
		Description: "Fetch the vulnerability scan report for a specific " +
			"image digest. Returns severity counts, top-10 CVEs (id, " +
			"severity, package, fixed_in), and whether an SBOM was " +
			"generated. Use this to answer 'which of our images have " +
			"log4j 2.14?' or 'what critical CVEs are in prod/api's " +
			"latest?'.",
		InputSchema: mustJSON(`{
			"type": "object",
			"required": ["org", "repo", "digest"],
			"properties": {
				"org":    {"type": "string"},
				"repo":   {"type": "string"},
				"digest": {"type": "string", "description": "sha256:<64-hex>"}
			}
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		org := argStr(req.Params.Arguments, "org")
		repo := argStr(req.Params.Arguments, "repo")
		digest := argStr(req.Params.Arguments, "digest")
		if org == "" || repo == "" || digest == "" {
			return r.errorResult("get_scan_report", fmt.Errorf("org, repo, and digest are required")), nil
		}
		rep, err := r.client.GetScanReport(ctx, org, repo, digest)
		if err != nil {
			r.logger.Error("get_scan_report failed", "err", err, "org", org, "repo", repo, "digest", digest)
			return r.errorResult("get_scan_report", err), nil
		}
		summary := fmt.Sprintf(
			"Scan report for %s: critical=%d high=%d medium=%d low=%d, SBOM=%v.",
			rep.Digest, rep.Severities.Critical, rep.Severities.High,
			rep.Severities.Medium, rep.Severities.Low, rep.SBOMPresent,
		)
		return jsonResult(summary, rep), nil
	})

	// ---- registry_list_signatures -----------------------------------
	s.AddTool(&mcp.Tool{
		Name: "registry_list_signatures",
		Description: "List cryptographic signatures attached to an image " +
			"digest (Cosign or Notary v2). Returns each signature's key " +
			"id, algorithm, signer identity, and backend. Use this to " +
			"answer 'is prod/api:v1 signed?' or 'who signed this image?'.",
		InputSchema: mustJSON(`{
			"type": "object",
			"required": ["org", "repo", "digest"],
			"properties": {
				"org":    {"type": "string"},
				"repo":   {"type": "string"},
				"digest": {"type": "string", "description": "sha256:<64-hex>"}
			}
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		org := argStr(req.Params.Arguments, "org")
		repo := argStr(req.Params.Arguments, "repo")
		digest := argStr(req.Params.Arguments, "digest")
		if org == "" || repo == "" || digest == "" {
			return r.errorResult("list_signatures", fmt.Errorf("org, repo, and digest are required")), nil
		}
		sigs, err := r.client.ListSignatures(ctx, org, repo, digest)
		if err != nil {
			r.logger.Error("list_signatures failed", "err", err, "org", org, "repo", repo, "digest", digest)
			return r.errorResult("list_signatures", err), nil
		}
		return jsonResult(fmt.Sprintf("Found %d signatures for %s.", len(sigs), digest), sigs), nil
	})
}

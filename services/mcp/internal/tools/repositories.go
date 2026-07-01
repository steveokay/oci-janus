package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/steveokay/oci-janus/services/mcp/internal/client"
)

// registerRepositories wires the 4 repository-surface tools:
// list_repositories, list_tags, get_manifest, list_promotions.
// (list_promotions has its own file for reasons — it's a FUT-020
// soft-dep, see promotions.go.)
func (r *Registry) registerRepositories(s *mcp.Server) {
	// ---- registry_list_repositories ---------------------------------
	s.AddTool(&mcp.Tool{
		Name: "registry_list_repositories",
		Description: "List the registry's repositories, optionally filtered " +
			"by organisation. Returns each repo's name, org, creation " +
			"time, and whether tags are immutable / require a signature. " +
			"Use this to answer 'what repos do we have?' or 'what's in " +
			"the prod org?'.",
		InputSchema: mustJSON(`{
			"type": "object",
			"properties": {
				"org": {
					"type": "string",
					"description": "Filter to a single organisation. Omit for all orgs the API key can see."
				}
			}
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		org := argStr(req.Params.Arguments, "org")
		repos, err := r.client.ListRepositories(ctx, org)
		if err != nil {
			r.logger.Error("list_repositories failed", "err", err, "org", org)
			return r.errorResult("list_repositories", err), nil
		}
		prefix := fmt.Sprintf("Found %d repositories.", len(repos))
		if org != "" {
			prefix = fmt.Sprintf("Found %d repositories in org %q.", len(repos), org)
		}
		return jsonResult(prefix, repos), nil
	})

	// ---- registry_list_tags -----------------------------------------
	s.AddTool(&mcp.Tool{
		Name: "registry_list_tags",
		Description: "List every tag in a single repository. Returns each " +
			"tag's name, manifest digest, size in bytes, and last-pulled " +
			"timestamp. Use this to answer 'what tags does prod/api have?' " +
			"or 'when was v1.2.3 last pulled?'.",
		InputSchema: mustJSON(`{
			"type": "object",
			"required": ["org", "repo"],
			"properties": {
				"org":  {"type": "string", "description": "Organisation name."},
				"repo": {"type": "string", "description": "Repository name inside the org."}
			}
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		org := argStr(req.Params.Arguments, "org")
		repo := argStr(req.Params.Arguments, "repo")
		if org == "" || repo == "" {
			return r.errorResult("list_tags", fmt.Errorf("both org and repo are required")), nil
		}
		tags, err := r.client.ListTags(ctx, org, repo)
		if err != nil {
			r.logger.Error("list_tags failed", "err", err, "org", org, "repo", repo)
			return r.errorResult("list_tags", err), nil
		}
		return jsonResult(fmt.Sprintf("Found %d tags in %s/%s.", len(tags), org, repo), tags), nil
	})

	// ---- registry_get_manifest --------------------------------------
	s.AddTool(&mcp.Tool{
		Name: "registry_get_manifest",
		Description: "Fetch the OCI manifest for a single tag. Returns the " +
			"manifest's media type, digest, and layer list. Use this to " +
			"answer 'what layers does prod/api:v1 have?' or 'what's the " +
			"digest of latest?'.",
		InputSchema: mustJSON(`{
			"type": "object",
			"required": ["org", "repo", "tag"],
			"properties": {
				"org":  {"type": "string"},
				"repo": {"type": "string"},
				"tag":  {"type": "string", "description": "Tag name or digest."}
			}
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		org := argStr(req.Params.Arguments, "org")
		repo := argStr(req.Params.Arguments, "repo")
		tag := argStr(req.Params.Arguments, "tag")
		if org == "" || repo == "" || tag == "" {
			return r.errorResult("get_manifest", fmt.Errorf("org, repo, and tag are required")), nil
		}
		m, err := r.client.GetManifest(ctx, org, repo, tag)
		if err != nil {
			r.logger.Error("get_manifest failed", "err", err, "org", org, "repo", repo, "tag", tag)
			return r.errorResult("get_manifest", err), nil
		}
		// Compute a small summary that reads well in LLM output.
		summary := manifestSummary(m)
		body, mErr := json.MarshalIndent(m, "", "  ")
		if mErr != nil {
			// This branch shouldn't fire — Manifest is well-formed.
			return r.errorResult("get_manifest", mErr), nil
		}
		text := fmt.Sprintf("%s\n\n```json\n%s\n```", summary, string(body))
		return textResult(text), nil
	})
}

// manifestSummary computes the human-readable summary shown above the
// raw manifest JSON. Split into its own function so a Wave 2 refactor
// (e.g. adding attestation info) has a natural extension point.
func manifestSummary(m *client.Manifest) string {
	total := int64(0)
	for _, l := range m.Layers {
		total += l.SizeBytes
	}
	return fmt.Sprintf(
		"Manifest %s: mediaType=%s, layers=%d, total layer bytes=%d.",
		m.Digest, m.MediaType, len(m.Layers), total,
	)
}

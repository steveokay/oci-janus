// Package handler — stats_storage.go
//
// FE-API-031 — GET /api/v1/stats/storage
//
// Per-repo storage breakdown for the calling tenant: tenant total + top-50
// repos sorted by storage_used DESC. Lets a tenant admin answer "where is
// my storage going" without paging through /repositories.
//
// Auth: any authenticated tenant member. This is workspace metadata, not
// a destructive action — same gate as /workspace/me.
//
// Lives in its own file so concurrent edits to handler.go don't conflict
// with the storage surface.
package handler

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
	"google.golang.org/grpc/codes"
)

// RepositoryStorageEntry is one row in the StorageBreakdownResponse.
//
// REM-013 gap 3 — RetentionSummary + RetentionSource surface the
// effective retention policy on each row so the dashboard's storage
// breakdown card can render a per-row "Retention" column without a
// per-row follow-up call from the FE. Source distinguishes a per-repo
// override from an inherited org default; an empty value means "no
// policy anywhere", matching the FE-API-037 GET's NotFound semantics.
type RepositoryStorageEntry struct {
	RepoID           string  `json:"repo_id"`
	Org              string  `json:"org"`
	Name             string  `json:"name"`
	StorageUsedBytes int64   `json:"storage_used_bytes"`
	PercentOfTenant  float64 `json:"percent_of_tenant"`
	// RetentionSummary is a 1-line operator-readable summary of the
	// effective policy ("max 50 manifests · 30d") — empty when no
	// policy applies. The dashboard renders this verbatim alongside a
	// link to the repo's Retention tab.
	RetentionSummary string `json:"retention_summary,omitempty"`
	// RetentionSource is one of: "" (no policy), "repo" (per-repo
	// override), "org" (inherited org default). Mirrors the
	// inherited_from label the FE-API-037 GET emits.
	RetentionSource string `json:"retention_source,omitempty"`
}

// StorageBreakdownResponse is the JSON body of GET /api/v1/stats/storage.
//
// `tenant_storage_used_bytes` is the sum across ALL repos in the tenant,
// not just the top-50 returned in `repositories`. Percent values in each
// entry are computed against this total so they sum to ≤100 for the top-50.
type StorageBreakdownResponse struct {
	TenantStorageUsedBytes int64                    `json:"tenant_storage_used_bytes"`
	Repositories           []RepositoryStorageEntry `json:"repositories"`
}

func (h *Handler) handleGetStorageBreakdown(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())

	resp, err := h.meta.GetTenantStorageBreakdown(r.Context(), &metadatav1.GetTenantStorageBreakdownRequest{
		TenantId: tenantID,
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "GetTenantStorageBreakdown", "err", err, "tenant_id", tenantID)
		writeError(w, http.StatusInternalServerError, "failed to fetch storage breakdown")
		return
	}

	// Always emit a non-nil slice on the wire so the dashboard's serde has
	// a stable shape even for a zero-repo tenant.
	out := StorageBreakdownResponse{
		TenantStorageUsedBytes: resp.GetTenantStorageUsedBytes(),
		Repositories:           make([]RepositoryStorageEntry, 0, len(resp.GetRepositories())),
	}
	for _, e := range resp.GetRepositories() {
		entry := RepositoryStorageEntry{
			RepoID:           e.GetRepoId(),
			Org:              e.GetOrg(),
			Name:             e.GetName(),
			StorageUsedBytes: e.GetStorageUsedBytes(),
			PercentOfTenant:  e.GetPercentOfTenant(),
		}
		// REM-013 gap 3 — fan-out: ask metadata for the effective
		// retention policy on every row so the dashboard's Retention
		// column has source-of-truth data without a per-row FE call.
		// Top-50 is the cap server-side, so this is bounded; if
		// profiling shows it slow we can promote to a batch RPC.
		summarizeRetention(r, h, tenantID, entry.RepoID, &entry)
		out.Repositories = append(out.Repositories, entry)
	}

	writeJSON(w, http.StatusOK, out)
}

// summarizeRetention populates the entry's RetentionSummary +
// RetentionSource fields from a per-repo GetEffectiveRetentionPolicy
// call. Failures are silently ignored (logged but not surfaced) so a
// transient metadata blip on one repo doesn't take down the whole
// breakdown response. Falls back to empty fields, which the dashboard
// renders as "—".
func summarizeRetention(r *http.Request, h *Handler, tenantID, repoID string, entry *RepositoryStorageEntry) {
	eff, err := h.meta.GetEffectiveRetentionPolicy(r.Context(), &metadatav1.GetEffectiveRetentionPolicyRequest{
		TenantId: tenantID,
		RepoId:   repoID,
	})
	if err != nil {
		if grpcCodeOf(err) == codes.NotFound {
			// No per-repo and no org default — leave the entry empty
			// so the wire shape stays consistent with "—" on the FE.
			return
		}
		// Unexpected error — log but don't fail the breakdown. The
		// dashboard already handles missing retention metadata; the
		// operator can still see storage stats.
		slog.WarnContext(r.Context(),
			"summarizeRetention: GetEffectiveRetentionPolicy failed",
			"err", err, "repo_id", repoID)
		return
	}
	policy := eff.GetPolicy()
	if policy == nil || !policy.GetEnabled() {
		// Disabled policies don't propagate per FE-API-039 semantics;
		// match that by treating it as "no effective retention".
		return
	}
	entry.RetentionSource = eff.GetInheritedFrom()
	if entry.RetentionSource == "" {
		// Pre-FE-API-039 servers may omit the field; the per-repo
		// shape defaults to "repo" by convention.
		entry.RetentionSource = "repo"
	}
	entry.RetentionSummary = describeEffectivePolicy(policy)
}

// describeEffectivePolicy renders a compact "max 50 manifests · 30d"
// summary string. Only the first two rule kinds surface to keep the
// column readable; operators can click through to the repo Retention
// tab for the full breakdown.
func describeEffectivePolicy(p *metadatav1.RetentionPolicy) string {
	rules := p.GetRules()
	if len(rules) == 0 {
		// Enabled with no rules is a no-op policy — still report it
		// so the dashboard distinguishes "policy exists but does
		// nothing" from "no policy".
		return "no rules"
	}
	parts := make([]string, 0, 2)
	for i, rule := range rules {
		if i >= 2 {
			parts = append(parts, fmt.Sprintf("+%d more", len(rules)-2))
			break
		}
		parts = append(parts, ruleShortForm(rule))
	}
	return strings.Join(parts, " · ")
}

// ruleShortForm formats one rule the way it'd read on a sparkline
// chip — short enough for a table cell, distinct enough that an
// operator scanning the column can guess what each repo is doing.
func ruleShortForm(rule *metadatav1.RetentionRule) string {
	switch rule.GetKind() {
	case "max_age_days":
		return fmt.Sprintf("%dd age", rule.GetValue())
	case "max_count":
		return fmt.Sprintf("%d manifests", rule.GetValue())
	case "max_size_bytes":
		return fmt.Sprintf("%d bytes", rule.GetValue())
	case "dangling_grace_days":
		return fmt.Sprintf("%dd dangle", rule.GetValue())
	case "max_idle_days":
		return fmt.Sprintf("%dd idle", rule.GetValue())
	default:
		return rule.GetKind()
	}
}

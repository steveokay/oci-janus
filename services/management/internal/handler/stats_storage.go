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
	"log/slog"
	"net/http"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// RepositoryStorageEntry is one row in the StorageBreakdownResponse.
type RepositoryStorageEntry struct {
	RepoID           string  `json:"repo_id"`
	Org              string  `json:"org"`
	Name             string  `json:"name"`
	StorageUsedBytes int64   `json:"storage_used_bytes"`
	PercentOfTenant  float64 `json:"percent_of_tenant"`
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
		out.Repositories = append(out.Repositories, RepositoryStorageEntry{
			RepoID:           e.GetRepoId(),
			Org:              e.GetOrg(),
			Name:             e.GetName(),
			StorageUsedBytes: e.GetStorageUsedBytes(),
			PercentOfTenant:  e.GetPercentOfTenant(),
		})
	}

	writeJSON(w, http.StatusOK, out)
}

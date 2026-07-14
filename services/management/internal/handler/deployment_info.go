package handler

import (
	"encoding/json"
	"net/http"
)

// handleDeploymentInfo returns the deployment's build version.
//
// Public + unauthenticated by design — leaks NO tenant data, only the binary's
// build metadata. Cached aggressively by the FE. The platform is single-tenant
// only (ADR-0031), so the historical `deployment_mode` field was removed — the
// FE no longer branches on it.
func (h *Handler) handleDeploymentInfo(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{
		"version": h.buildVersion,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

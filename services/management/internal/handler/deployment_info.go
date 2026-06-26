package handler

import (
	"encoding/json"
	"net/http"
)

// HandleDeploymentInfo returns the deployment posture the FE needs to decide
// which chrome to render (tenant switcher, plan badge, signup form, etc.).
//
// Public + unauthenticated by design — leaks NO tenant data, only the binary's
// build metadata + DEPLOYMENT_MODE. Cached aggressively by the FE.
//
// Phase 1.4 of REDESIGN-001. See CLAUDE.md decision log.
func (h *Handler) HandleDeploymentInfo(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{
		"deployment_mode": string(h.deploymentMode),
		"version":         h.buildVersion,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

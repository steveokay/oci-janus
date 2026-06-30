package handler

import (
	"encoding/json"
	"net/http"
)

// handleRegistryInfo returns the deployment's externally-reachable registry
// hostname so the FE credential-helpers surface (/api-keys/helpers) can
// render copy-paste-ready `docker login`, k8s Secret, Terraform, and GHA
// snippets without operators having to type the hostname.
//
// Auth-gated (requireAuth). The hostname itself is publicly discoverable —
// it's the URL operators push/pull against — but the helpers page lives
// behind /api-keys/helpers which is auth-gated, so the matching BFF route
// is too. Spec compliance over symmetry with /deployment-info.
// Cached aggressively by the FE (the hostname doesn't change during a session).
//
// Returns 500 with a clear error body when PLATFORM_HOST is empty, rather
// than returning an empty string the FE would render as "docker login  "
// (two spaces). The config layer's production validator catches this at
// startup; this guard handles the dev-misconfig case.
//
// FUT-002 — see docs/superpowers/specs/2026-06-30-api-keys-tier2-backend-design.md.
func (h *Handler) handleRegistryInfo(w http.ResponseWriter, r *http.Request) {
	if h.platformHost == "" {
		http.Error(w, `{"error":"PLATFORM_HOST not configured"}`, http.StatusInternalServerError)
		return
	}
	body := map[string]any{
		"registry_host":     h.platformHost,
		"supports_oci_v1_1": true,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

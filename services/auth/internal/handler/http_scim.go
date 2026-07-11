package handler

import (
	"encoding/json"
	"net/http"

	"github.com/steveokay/oci-janus/libs/auth/bearer"
)

// scimVerifier verifies a raw SCIM bearer token. Returns (true, nil) only when
// the config is enabled and the token matches.
type scimVerifier func(raw string) (bool, error)

// requireSCIMAuth gates a SCIM handler on the global SCIM token. It is NOT the
// user auth path: the SCIM principal carries no RBAC roles and is valid only on
// /scim/v2/*. Fail-closed — any error or mismatch is a 401. It never returns an
// oracle distinguishing "no token", "wrong token", or "feature disabled".
func requireSCIMAuth(verify scimVerifier, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, ok := bearer.Extract(r.Header.Get("Authorization"))
		if !ok || raw == "" {
			writeSCIMError(w, http.StatusUnauthorized, "", "missing or malformed bearer token")
			return
		}
		valid, err := verify(raw)
		if err != nil || !valid {
			writeSCIMError(w, http.StatusUnauthorized, "", "invalid SCIM token")
			return
		}
		next(w, r)
	}
}

// writeSCIMJSON marshals v with the application/scim+json content type.
func writeSCIMJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/scim+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ── Discovery (static JSON, RFC 7643 §5-6) ──────────────────────────────────

// scimServiceProviderConfig advertises the SCIM capabilities this deployment
// supports so an IdP can auto-configure. Static — never changes at runtime.
func (h *HTTPHandler) scimServiceProviderConfig(w http.ResponseWriter, _ *http.Request) {
	cfg := map[string]any{
		"schemas":        []string{"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"},
		"patch":          map[string]any{"supported": true},
		"bulk":           map[string]any{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"filter":         map[string]any{"supported": true, "maxResults": 200},
		"changePassword": map[string]any{"supported": false},
		"sort":           map[string]any{"supported": false},
		"etag":           map[string]any{"supported": false},
		"authenticationSchemes": []map[string]any{{
			"type":        "oauthbearertoken",
			"name":        "OAuth Bearer Token",
			"description": "Authentication via the deployment SCIM bearer token.",
		}},
	}
	writeSCIMJSON(w, http.StatusOK, cfg)
}

// scimResourceTypes returns the single User resource type (Users-only v1).
func (h *HTTPHandler) scimResourceTypes(w http.ResponseWriter, _ *http.Request) {
	userType := map[string]any{
		"schemas":     []string{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"},
		"id":          "User",
		"name":        "User",
		"endpoint":    "/Users",
		"description": "User Account",
		"schema":      "urn:ietf:params:scim:schemas:core:2.0:User",
		"meta": map[string]any{
			"resourceType": "ResourceType",
			"location":     "/scim/v2/ResourceTypes/User",
		},
	}
	resp := map[string]any{
		"schemas":      []string{"urn:ietf:params:scim:api:messages:2.0:ListResponse"},
		"totalResults": 1,
		"startIndex":   1,
		"itemsPerPage": 1,
		"Resources":    []map[string]any{userType},
	}
	writeSCIMJSON(w, http.StatusOK, resp)
}

// scimSchemas returns the minimal core:2.0:User schema (the subset of attributes
// this v1 supports: userName, name.formatted, displayName, emails, active,
// externalId). externalId is a common attribute (top-level), not listed here.
func (h *HTTPHandler) scimSchemas(w http.ResponseWriter, _ *http.Request) {
	userSchema := map[string]any{
		"schemas":     []string{"urn:ietf:params:scim:schemas:core:2.0:Schema"},
		"id":          "urn:ietf:params:scim:schemas:core:2.0:User",
		"name":        "User",
		"description": "User Account",
		"attributes": []map[string]any{
			{"name": "userName", "type": "string", "multiValued": false, "required": true, "caseExact": false, "mutability": "readWrite", "uniqueness": "server"},
			{"name": "displayName", "type": "string", "multiValued": false, "required": false, "caseExact": false, "mutability": "readWrite"},
			{"name": "name", "type": "complex", "multiValued": false, "required": false, "subAttributes": []map[string]any{
				{"name": "formatted", "type": "string", "multiValued": false, "required": false, "caseExact": false, "mutability": "readWrite"},
			}},
			{"name": "emails", "type": "complex", "multiValued": true, "required": false, "subAttributes": []map[string]any{
				{"name": "value", "type": "string", "multiValued": false, "required": false, "caseExact": false, "mutability": "readWrite"},
				{"name": "primary", "type": "boolean", "multiValued": false, "required": false, "mutability": "readWrite"},
			}},
			{"name": "active", "type": "boolean", "multiValued": false, "required": false, "mutability": "readWrite"},
		},
		"meta": map[string]any{
			"resourceType": "Schema",
			"location":     "/scim/v2/Schemas/urn:ietf:params:scim:schemas:core:2.0:User",
		},
	}
	resp := map[string]any{
		"schemas":      []string{"urn:ietf:params:scim:api:messages:2.0:ListResponse"},
		"totalResults": 1,
		"startIndex":   1,
		"itemsPerPage": 1,
		"Resources":    []map[string]any{userSchema},
	}
	writeSCIMJSON(w, http.StatusOK, resp)
}

// RegisterSCIM mounts all /scim/v2/* routes, each gated by requireSCIMAuth.
// verify is the SCIM token verifier (Service.VerifySCIMToken via the wrapper in
// http.go). Users routes are added in Phase 2.
func (h *HTTPHandler) RegisterSCIM(mux *http.ServeMux, verify scimVerifier) {
	g := func(fn http.HandlerFunc) http.HandlerFunc { return requireSCIMAuth(verify, fn) }
	mux.HandleFunc("GET /scim/v2/ServiceProviderConfig", g(h.scimServiceProviderConfig))
	mux.HandleFunc("GET /scim/v2/ResourceTypes", g(h.scimResourceTypes))
	mux.HandleFunc("GET /scim/v2/Schemas", g(h.scimSchemas))
	// Users routes are added in Phase 2 (Task 11).
}

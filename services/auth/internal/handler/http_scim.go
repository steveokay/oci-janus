package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/libs/auth/bearer"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
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

// ── Users (RFC 7644 §3.3-3.6) ───────────────────────────────────────────────

// scimCreateUser handles POST /scim/v2/Users — provision (or link) a user.
// 201 with the created resource on success; 409 (uniqueness) on a local-password
// collision (D3); 400 (invalidValue) on a missing userName/email.
func (h *HTTPHandler) scimCreateUser(w http.ResponseWriter, r *http.Request) {
	var body scimUser
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeSCIMError(w, http.StatusBadRequest, "invalidValue", "malformed JSON body")
		return
	}
	email := body.primaryEmail()
	if body.UserName == "" && email == "" {
		writeSCIMError(w, http.StatusBadRequest, "invalidValue", "userName or an email is required")
		return
	}
	res, err := h.svc.Provision(r.Context(), service.ScimProvisionInput{
		Email:       email,
		UserName:    body.UserName,
		DisplayName: body.DisplayName,
		ExternalID:  body.ExternalID,
	})
	if err != nil {
		if errors.Is(err, service.ErrSCIMConflict) {
			writeSCIMError(w, http.StatusConflict, "uniqueness", "a user with this email already has a local password")
			return
		}
		writeSCIMError(w, http.StatusInternalServerError, "", "failed to provision user")
		return
	}
	writeSCIMJSON(w, http.StatusCreated, toSCIMUser(res.User, scimExtID(res.User, body.ExternalID)))
}

// scimListUsers handles GET /scim/v2/Users — paged + filtered list. The IdP
// polls this with `filter=userName eq "x"` (or externalId) to reconcile state.
func (h *HTTPHandler) scimListUsers(w http.ResponseWriter, r *http.Request) {
	byUsername, byExternalID, active, err := parseUserFilter(r.URL.Query().Get("filter"))
	if err != nil {
		writeSCIMError(w, http.StatusBadRequest, "invalidFilter", err.Error())
		return
	}
	startIndex := atoiDefault(r.URL.Query().Get("startIndex"), 1)
	count := atoiDefault(r.URL.Query().Get("count"), 200)

	users, total, err := h.svc.ListSCIMUsers(r.Context(), byUsername, byExternalID, active, startIndex, count)
	if err != nil {
		writeSCIMError(w, http.StatusInternalServerError, "", "failed to list users")
		return
	}
	resources := make([]scimUser, 0, len(users))
	for _, u := range users {
		resources = append(resources, toSCIMUser(u, u.ExternalID))
	}
	writeSCIMJSON(w, http.StatusOK, scimListResponse{
		Schemas:      []string{"urn:ietf:params:scim:api:messages:2.0:ListResponse"},
		TotalResults: total,
		StartIndex:   startIndex,
		ItemsPerPage: len(resources),
		Resources:    resources,
	})
}

// scimGetUser handles GET /scim/v2/Users/{id}.
func (h *HTTPHandler) scimGetUser(w http.ResponseWriter, r *http.Request) {
	id, ok := scimPathID(w, r)
	if !ok {
		return
	}
	u, err := h.svc.GetSCIMUserByID(r.Context(), id)
	if err != nil {
		writeSCIMNotFoundOr500(w, err)
		return
	}
	writeSCIMJSON(w, http.StatusOK, toSCIMUser(u, u.ExternalID))
}

// scimPutUser handles PUT /scim/v2/Users/{id} — full replace. v1 honours the
// `active` field (so a PUT-based deprovision works) and otherwise re-reads the
// user; other attribute replacement is not yet supported (501).
func (h *HTTPHandler) scimPutUser(w http.ResponseWriter, r *http.Request) {
	id, ok := scimPathID(w, r)
	if !ok {
		return
	}
	var body scimUser
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeSCIMError(w, http.StatusBadRequest, "invalidValue", "malformed JSON body")
		return
	}
	// Apply the active flag (the one replace op we support in v1).
	if err := h.svc.SetActive(r.Context(), id, body.Active); err != nil {
		writeSCIMNotFoundOr500(w, err)
		return
	}
	u, err := h.svc.GetSCIMUserByID(r.Context(), id)
	if err != nil {
		writeSCIMNotFoundOr500(w, err)
		return
	}
	writeSCIMJSON(w, http.StatusOK, toSCIMUser(u, u.ExternalID))
}

// scimPatchUser handles PATCH /scim/v2/Users/{id}. Only the `active` replace op
// is honoured in v1 (the deprovision/reprovision path Okta/Entra send); any
// other op path returns 501 Not Implemented.
func (h *HTTPHandler) scimPatchUser(w http.ResponseWriter, r *http.Request) {
	id, ok := scimPathID(w, r)
	if !ok {
		return
	}
	var body scimPatchRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeSCIMError(w, http.StatusBadRequest, "invalidValue", "malformed PATCH body")
		return
	}
	handledActive := false
	for _, op := range body.Operations {
		// Okta/Entra send op="replace" (case-insensitive) targeting the `active`
		// attribute, either as path="active" with value=bool, or path="" with
		// value={"active":bool}.
		active, isActiveOp, perr := extractActiveOp(op)
		if perr != nil {
			writeSCIMError(w, http.StatusBadRequest, "invalidValue", perr.Error())
			return
		}
		if !isActiveOp {
			writeSCIMError(w, http.StatusNotImplemented, "", "only the active replace op is supported in v1")
			return
		}
		if err := h.svc.SetActive(r.Context(), id, active); err != nil {
			writeSCIMNotFoundOr500(w, err)
			return
		}
		handledActive = true
	}
	if !handledActive {
		writeSCIMError(w, http.StatusBadRequest, "invalidValue", "no supported operations in PATCH body")
		return
	}
	u, err := h.svc.GetSCIMUserByID(r.Context(), id)
	if err != nil {
		writeSCIMNotFoundOr500(w, err)
		return
	}
	writeSCIMJSON(w, http.StatusOK, toSCIMUser(u, u.ExternalID))
}

// scimDeleteUser handles DELETE /scim/v2/Users/{id}. Per spec D4 we DISABLE
// (deactivate) rather than hard-delete so the audit trail + hash chain stay
// intact; a 204 is returned as SCIM expects.
func (h *HTTPHandler) scimDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := scimPathID(w, r)
	if !ok {
		return
	}
	if err := h.svc.SetActive(r.Context(), id, false); err != nil {
		writeSCIMNotFoundOr500(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Users helpers ───────────────────────────────────────────────────────────

// scimPathID parses the {id} path value as a UUID, writing a 400 invalidValue
// and returning ok=false on a malformed id.
func scimPathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeSCIMError(w, http.StatusBadRequest, "invalidValue", "malformed user id")
		return uuid.Nil, false
	}
	return id, true
}

// scimExtID prefers the request-body external id (create path) and otherwise
// falls back to the id the repository echoed onto the user.
func scimExtID(u *repository.User, hint string) string {
	if hint != "" {
		return hint
	}
	return u.ExternalID
}

// writeSCIMNotFoundOr500 maps repository.ErrNotFound → 404 and everything else
// to a 500, both in the SCIM error envelope.
func writeSCIMNotFoundOr500(w http.ResponseWriter, err error) {
	if errors.Is(err, repository.ErrNotFound) {
		writeSCIMError(w, http.StatusNotFound, "", "user not found")
		return
	}
	writeSCIMError(w, http.StatusInternalServerError, "", "internal error")
}

// atoiDefault parses s as an int, returning def on empty/invalid input.
func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// extractActiveOp interprets a single PATCH op. It returns (active, true, nil)
// when the op targets the `active` attribute (path="active" with a bool value,
// or path="" with a {"active":bool} value); (_, false, nil) when the op targets
// something else; and an error only when an active op's value is unparseable.
func extractActiveOp(op scimPatchOp) (active bool, isActive bool, err error) {
	if !strings.EqualFold(op.Op, "replace") && !strings.EqualFold(op.Op, "add") {
		// remove of active etc. is not something Okta/Entra send for deprovision.
		return false, false, nil
	}
	if strings.EqualFold(op.Path, "active") {
		var b bool
		if uerr := json.Unmarshal(op.Value, &b); uerr != nil {
			return false, false, errInvalidActiveValue
		}
		return b, true, nil
	}
	if op.Path == "" {
		// No-path replace: value is an object; look for "active".
		var m map[string]json.RawMessage
		if uerr := json.Unmarshal(op.Value, &m); uerr != nil {
			return false, false, nil // not an active op we recognise
		}
		if raw, present := m["active"]; present {
			var b bool
			if uerr := json.Unmarshal(raw, &b); uerr != nil {
				return false, false, errInvalidActiveValue
			}
			return b, true, nil
		}
	}
	return false, false, nil
}

var errInvalidActiveValue = errors.New("active value must be a boolean")

// RegisterSCIM mounts all /scim/v2/* routes, each gated by requireSCIMAuth.
// verify is the SCIM token verifier (Service.VerifySCIMToken via the wrapper in
// http.go).
func (h *HTTPHandler) RegisterSCIM(mux *http.ServeMux, verify scimVerifier) {
	g := func(fn http.HandlerFunc) http.HandlerFunc { return requireSCIMAuth(verify, fn) }
	mux.HandleFunc("GET /scim/v2/ServiceProviderConfig", g(h.scimServiceProviderConfig))
	mux.HandleFunc("GET /scim/v2/ResourceTypes", g(h.scimResourceTypes))
	mux.HandleFunc("GET /scim/v2/Schemas", g(h.scimSchemas))
	mux.HandleFunc("POST /scim/v2/Users", g(h.scimCreateUser))
	mux.HandleFunc("GET /scim/v2/Users", g(h.scimListUsers))
	mux.HandleFunc("GET /scim/v2/Users/{id}", g(h.scimGetUser))
	mux.HandleFunc("PUT /scim/v2/Users/{id}", g(h.scimPutUser))
	mux.HandleFunc("PATCH /scim/v2/Users/{id}", g(h.scimPatchUser))
	mux.HandleFunc("DELETE /scim/v2/Users/{id}", g(h.scimDeleteUser))
}

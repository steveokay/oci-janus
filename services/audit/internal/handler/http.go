// Package handler provides HTTP endpoints for writing and querying audit events.
// POST /audit/events  — called by internal services to record synchronous audit events
// GET  /audit/events  — query audit trail by tenant (requires X-Tenant-ID header)
package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// HTTPHandler wires the audit HTTP endpoints.
type HTTPHandler struct {
	repo *repository.Repository
}

// New creates an HTTPHandler.
func New(repo *repository.Repository) *HTTPHandler {
	return &HTTPHandler{repo: repo}
}

// writeAuditRequest is the JSON body for POST /audit/events.
type writeAuditRequest struct {
	TenantID  string          `json:"tenant_id"`
	ActorID   string          `json:"actor_id"`
	ActorType string          `json:"actor_type"`
	ActorIP   string          `json:"actor_ip"`
	Action    string          `json:"action"`
	Resource  string          `json:"resource"`
	Outcome   string          `json:"outcome"`
	Metadata  json.RawMessage `json:"metadata"`
}

// WriteEvent handles POST /audit/events.
// Used by other internal services (e.g. auth) to record synchronous audit events.
func (h *HTTPHandler) WriteEvent(w http.ResponseWriter, r *http.Request) {
	var req writeAuditRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	tenantID, err := uuid.Parse(req.TenantID)
	if err != nil {
		http.Error(w, "invalid tenant_id", http.StatusBadRequest)
		return
	}
	if req.ActorID == "" || req.Action == "" || req.Outcome == "" {
		http.Error(w, "actor_id, action, and outcome are required", http.StatusBadRequest)
		return
	}
	if req.ActorType != "user" && req.ActorType != "robot" && req.ActorType != "system" {
		req.ActorType = "system"
	}

	ae := &repository.AuditEvent{
		TenantID:   tenantID,
		ActorID:    req.ActorID,
		ActorType:  req.ActorType,
		ActorIP:    req.ActorIP,
		Action:     req.Action,
		Resource:   req.Resource,
		Outcome:    req.Outcome,
		Metadata:   req.Metadata,
		OccurredAt: time.Now(),
	}
	if ae.Metadata == nil {
		ae.Metadata = json.RawMessage("{}")
	}

	if err := h.repo.Insert(r.Context(), ae); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// QueryEvents handles GET /audit/events?tenant_id=...&from=...&to=...&actor_id=...&action=...
func (h *HTTPHandler) QueryEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	tenantID, err := uuid.Parse(q.Get("tenant_id"))
	if err != nil {
		http.Error(w, "tenant_id is required", http.StatusBadRequest)
		return
	}

	filter := repository.QueryFilter{
		TenantID: tenantID,
		ActorID:  q.Get("actor_id"),
		Action:   q.Get("action"),
	}

	if s := q.Get("from"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			filter.From = t
		}
	}
	if s := q.Get("to"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			filter.To = t
		}
	}
	if s := q.Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			filter.Limit = n
		}
	}
	if s := q.Get("offset"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			filter.Offset = n
		}
	}

	events, err := h.repo.Query(r.Context(), filter)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_ = json.NewEncoder(w).Encode(events)
}

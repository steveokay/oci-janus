// Package handler — scan history feed (FE-API-015).
//
// Mounted by handler.Register via the single registration line below to
// keep handler.go thin. Pure pass-through to registry-metadata; the
// ordering, cursor logic, and 30-day default live in the SQL repository.
package handler

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ScanHistoryEntryResponse is the JSON representation of one scan row.
type ScanHistoryEntryResponse struct {
	ScanID         string         `json:"scan_id"`
	Repo           string         `json:"repo"`
	Tag            string         `json:"tag"`
	ManifestDigest string         `json:"manifest_digest"`
	Scanner        string         `json:"scanner"`
	StartedAt      time.Time      `json:"started_at"`
	CompletedAt    *time.Time     `json:"completed_at"`
	Status         string         `json:"status"`
	SeverityCounts SeverityCounts `json:"severity_counts"`
	Trigger        string         `json:"trigger"`
}

// ScanHistoryListResponse is the JSON body for GET /api/v1/security/scans.
type ScanHistoryListResponse struct {
	Scans         []ScanHistoryEntryResponse `json:"scans"`
	NextPageToken string                     `json:"next_page_token"`
}

// handleListScanHistory backs GET /api/v1/security/scans.
//
// Query params:
//
//	since      optional RFC3339; default 30 days ago (applied server-side)
//	limit      optional, 1..200; default 50
//	page_token optional, opaque cursor from a previous response
//
// Tenant isolation is enforced upstream — the handler only forwards
// tenant_id from the authenticated context.
func (h *Handler) handleListScanHistory(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())

	req := &metadatav1.ListScanHistoryRequest{TenantId: tenantID}

	if s := r.URL.Query().Get("since"); s != "" {
		ts, err := time.Parse(time.RFC3339, s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since (expected RFC3339)")
			return
		}
		req.Since = timestamppb.New(ts)
	}

	pageToken := r.URL.Query().Get("page_token")
	if pageToken != "" {
		if err := validatePageToken(pageToken); err != nil {
			writeError(w, http.StatusBadRequest, "invalid page_token")
			return
		}
		req.PageToken = pageToken
	}

	// Default 50, hard cap 200 — matches FE-API-015 spec.
	limit := int32(50)
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			if n > 200 {
				n = 200
			}
			limit = int32(n)
		}
	}
	req.PageSize = limit

	resp, err := h.meta.ListScanHistory(r.Context(), req)
	if err != nil {
		slog.Error("ListScanHistory", "err", err, "tenant_id", tenantID)
		writeError(w, http.StatusInternalServerError, "failed to list scans")
		return
	}

	out := ScanHistoryListResponse{
		Scans:         make([]ScanHistoryEntryResponse, 0, len(resp.GetScans())),
		NextPageToken: resp.GetNextPageToken(),
	}
	for _, s := range resp.GetScans() {
		// completed_at is nullable so a pending / running scan surfaces as
		// JSON null instead of the Unix epoch. The pointer indirection on
		// the response struct keeps the wire shape honest.
		var completedAt *time.Time
		if ts := s.GetCompletedAt(); ts != nil {
			t := ts.AsTime()
			completedAt = &t
		}
		out.Scans = append(out.Scans, ScanHistoryEntryResponse{
			ScanID:         s.GetScanId(),
			Repo:           s.GetRepo(),
			Tag:            s.GetTag(),
			ManifestDigest: s.GetManifestDigest(),
			Scanner:        s.GetScanner(),
			StartedAt:      s.GetStartedAt().AsTime(),
			CompletedAt:    completedAt,
			Status:         s.GetStatus(),
			SeverityCounts: SeverityCounts{
				Critical:   s.GetSeverityCounts().GetCritical(),
				High:       s.GetSeverityCounts().GetHigh(),
				Medium:     s.GetSeverityCounts().GetMedium(),
				Low:        s.GetSeverityCounts().GetLow(),
				Negligible: s.GetSeverityCounts().GetNegligible(),
			},
			Trigger: s.GetTrigger(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

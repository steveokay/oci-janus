package handler

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// ---------------------------------------------------------------------------
// ListAuditEvents (FUT-082)
// ---------------------------------------------------------------------------

// TestListAuditEvents_validRequest_mapsRows covers the happy path: the handler
// wraps repository.Query and projects each *AuditEvent into an AuditEventRecord,
// copying every field except Metadata (which must never cross the wire).
func TestListAuditEvents_validRequest_mapsRows(t *testing.T) {
	tenantID := uuid.New()
	eventID := uuid.New()
	now := time.Now().UTC().Truncate(time.Microsecond)

	fake := &fakeRepo{
		queryEvents: []*repository.AuditEvent{
			{
				ID:         eventID,
				TenantID:   tenantID,
				ActorID:    "user-1",
				ActorType:  "user",
				ActorIP:    "10.0.0.1",
				Action:     "delete.tag",
				Resource:   "myorg/myrepo:v1",
				Outcome:    "success",
				Metadata:   json.RawMessage(`{"secret":"should-not-leak"}`),
				OccurredAt: now,
			},
		},
	}
	h := newHandler(fake)

	resp, err := h.ListAuditEvents(context.Background(), &auditv1.ListAuditEventsRequest{
		TenantId: tenantID.String(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetEvents()) != 1 {
		t.Fatalf("expected 1 event, got %d", len(resp.GetEvents()))
	}
	rec := resp.GetEvents()[0]
	if rec.GetId() != eventID.String() {
		t.Errorf("Id: got %q, want %q", rec.GetId(), eventID.String())
	}
	if rec.GetTenantId() != tenantID.String() {
		t.Errorf("TenantId: got %q, want %q", rec.GetTenantId(), tenantID.String())
	}
	if rec.GetActorId() != "user-1" {
		t.Errorf("ActorId: got %q, want user-1", rec.GetActorId())
	}
	if rec.GetActorType() != "user" {
		t.Errorf("ActorType: got %q, want user", rec.GetActorType())
	}
	if rec.GetActorIp() != "10.0.0.1" {
		t.Errorf("ActorIp: got %q, want 10.0.0.1", rec.GetActorIp())
	}
	if rec.GetAction() != "delete.tag" {
		t.Errorf("Action: got %q, want delete.tag", rec.GetAction())
	}
	if rec.GetResource() != "myorg/myrepo:v1" {
		t.Errorf("Resource: got %q, want myorg/myrepo:v1", rec.GetResource())
	}
	if rec.GetOutcome() != "success" {
		t.Errorf("Outcome: got %q, want success", rec.GetOutcome())
	}
	if ts := rec.GetOccurredAt(); ts == nil {
		t.Error("expected non-nil OccurredAt")
	} else if !ts.AsTime().Equal(now) {
		t.Errorf("OccurredAt: got %v, want %v", ts.AsTime(), now)
	}
}

// TestListAuditEvents_invalidTenantID_returnsInvalidArgument verifies the
// tenant_id shape check fires before any repository call.
func TestListAuditEvents_invalidTenantID_returnsInvalidArgument(t *testing.T) {
	fake := &fakeRepo{}
	h := newHandler(fake)

	_, err := h.ListAuditEvents(context.Background(), &auditv1.ListAuditEventsRequest{
		TenantId: "not-a-uuid",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
	if len(fake.queryCalls) != 0 {
		t.Errorf("expected no repo call on invalid tenant id, got %d", len(fake.queryCalls))
	}
}

// TestListAuditEvents_passesFiltersThrough asserts the handler maps actor_id,
// action, limit, and offset straight into the QueryFilter (the repository owns
// the Limit clamping, so the handler must not pre-clamp).
func TestListAuditEvents_passesFiltersThrough(t *testing.T) {
	tenantID := uuid.New()
	fake := &fakeRepo{}
	h := newHandler(fake)

	_, err := h.ListAuditEvents(context.Background(), &auditv1.ListAuditEventsRequest{
		TenantId: tenantID.String(),
		ActorId:  "user-42",
		Action:   "delete.tag",
		Limit:    25,
		Offset:   50,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.queryCalls) != 1 {
		t.Fatalf("expected 1 repo call, got %d", len(fake.queryCalls))
	}
	f := fake.queryCalls[0]
	if f.TenantID != tenantID {
		t.Errorf("TenantID: got %v, want %v", f.TenantID, tenantID)
	}
	if f.ActorID != "user-42" {
		t.Errorf("ActorID: got %q, want user-42", f.ActorID)
	}
	if f.Action != "delete.tag" {
		t.Errorf("Action: got %q, want delete.tag", f.Action)
	}
	if f.Limit != 25 {
		t.Errorf("Limit: got %d, want 25", f.Limit)
	}
	if f.Offset != 50 {
		t.Errorf("Offset: got %d, want 50", f.Offset)
	}
	// From/To must be left zero so the repository applies its own defaults.
	if !f.From.IsZero() || !f.To.IsZero() {
		t.Errorf("From/To must be zero, got From=%v To=%v", f.From, f.To)
	}
}

// TestListAuditEvents_emptyResult_returnsEmptySlice covers the no-rows case:
// the handler returns an empty (non-nil) Events slice, never an error.
func TestListAuditEvents_emptyResult_returnsEmptySlice(t *testing.T) {
	h := newHandler(&fakeRepo{queryEvents: nil})

	resp, err := h.ListAuditEvents(context.Background(), &auditv1.ListAuditEventsRequest{
		TenantId: uuid.New().String(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetEvents()) != 0 {
		t.Errorf("expected 0 events, got %d", len(resp.GetEvents()))
	}
}

// TestListAuditEvents_repoError_returnsInternal verifies that an underlying DB
// error is mapped through MapDBError (which yields a non-InvalidArgument code).
func TestListAuditEvents_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{queryErr: errors.New("db offline")})

	_, err := h.ListAuditEvents(context.Background(), &auditv1.ListAuditEventsRequest{
		TenantId: uuid.New().String(),
	})
	if status.Code(err) != codes.Internal {
		t.Errorf("expected Internal, got %v", err)
	}
}

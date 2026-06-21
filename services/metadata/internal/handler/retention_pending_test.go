// Tests for the FE-API-040 retention executor primitives on the metadata gRPC
// handler. We use the existing fakeRepo from grpc_test.go because these
// methods are thin forwarders — input validation + clamp logic is all the
// handler does, and the repository's SQL semantics are exercised by the
// integration test under services/metadata/internal/testutil/integration.
package handler

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
)

// ── MarkManifestRetentionPending ─────────────────────────────────────────────

// TestMarkPending_happyPath_forwardsCall verifies the handler forwards the
// (tenant_id, manifest_id) tuple to the repository unchanged.
func TestMarkPending_happyPath_forwardsCall(t *testing.T) {
	f := &fakeRepo{}
	h := newHandler(f)
	_, err := h.MarkManifestRetentionPending(context.Background(), &metadatav1.MarkManifestRetentionPendingRequest{
		TenantId:   "t1",
		ManifestId: "m1",
	})
	requireNoErr(t, err)
	if len(f.markPendingCalls) != 1 || f.markPendingCalls[0].tenantID != "t1" || f.markPendingCalls[0].manifestID != "m1" {
		t.Errorf("forwarded call mismatch: %+v", f.markPendingCalls)
	}
}

// TestMarkPending_missingTenant_returnsInvalidArgument verifies the handler
// rejects requests with an empty tenant_id without calling the repo.
func TestMarkPending_missingTenant_returnsInvalidArgument(t *testing.T) {
	f := &fakeRepo{}
	h := newHandler(f)
	_, err := h.MarkManifestRetentionPending(context.Background(), &metadatav1.MarkManifestRetentionPendingRequest{
		ManifestId: "m1",
	})
	requireCode(t, err, codes.InvalidArgument)
	if len(f.markPendingCalls) != 0 {
		t.Errorf("repo should not have been called: %+v", f.markPendingCalls)
	}
}

// TestMarkPending_missingManifest_returnsInvalidArgument is the symmetric case.
func TestMarkPending_missingManifest_returnsInvalidArgument(t *testing.T) {
	f := &fakeRepo{}
	h := newHandler(f)
	_, err := h.MarkManifestRetentionPending(context.Background(), &metadatav1.MarkManifestRetentionPendingRequest{
		TenantId: "t1",
	})
	requireCode(t, err, codes.InvalidArgument)
}

// TestMarkPending_repoNotFound_returnsNotFound verifies ErrNotFound bubbles
// through as gRPC NotFound (so the executor can skip a manifest that was
// already hard-deleted between EvaluateRetention and the soft-delete pass).
func TestMarkPending_repoNotFound_returnsNotFound(t *testing.T) {
	f := &fakeRepo{markPendingErr: repository.ErrNotFound}
	h := newHandler(f)
	_, err := h.MarkManifestRetentionPending(context.Background(), &metadatav1.MarkManifestRetentionPendingRequest{
		TenantId:   "t1",
		ManifestId: "m1",
	})
	requireCode(t, err, codes.NotFound)
}

// ── ClearManifestRetentionPending ────────────────────────────────────────────

func TestClearPending_happyPath_forwardsCall(t *testing.T) {
	f := &fakeRepo{}
	h := newHandler(f)
	_, err := h.ClearManifestRetentionPending(context.Background(), &metadatav1.ClearManifestRetentionPendingRequest{
		TenantId:   "t1",
		ManifestId: "m1",
	})
	requireNoErr(t, err)
	if len(f.clearPendingCalls) != 1 {
		t.Errorf("expected 1 clear call, got %d", len(f.clearPendingCalls))
	}
}

func TestClearPending_missingFields_returnInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	_, err := h.ClearManifestRetentionPending(context.Background(), &metadatav1.ClearManifestRetentionPendingRequest{})
	requireCode(t, err, codes.InvalidArgument)
}

// ── ListPendingDeleteManifests ───────────────────────────────────────────────

// TestListPending_clampsLimit verifies the handler clamps a runaway limit to
// the repository.MaxPendingDeleteLimit so a hostile caller cannot
// materialise an unbounded scan.
func TestListPending_clampsLimit(t *testing.T) {
	f := &fakeRepo{}
	h := newHandler(f)
	_, err := h.ListPendingDeleteManifests(context.Background(), &metadatav1.ListPendingDeleteManifestsRequest{
		TenantId: "t1",
		Limit:    1_000_000,
	})
	requireNoErr(t, err)
	if len(f.listPendingCalls) != 1 || f.listPendingCalls[0].limit != repository.MaxPendingDeleteLimit {
		t.Errorf("limit clamp: got %+v, want limit=%d", f.listPendingCalls, repository.MaxPendingDeleteLimit)
	}
}

// TestListPending_defaultsZeroLimit verifies a zero limit falls back to the
// repository default (proto field unset = zero value).
func TestListPending_defaultsZeroLimit(t *testing.T) {
	f := &fakeRepo{}
	h := newHandler(f)
	_, err := h.ListPendingDeleteManifests(context.Background(), &metadatav1.ListPendingDeleteManifestsRequest{
		TenantId: "t1",
	})
	requireNoErr(t, err)
	if f.listPendingCalls[0].limit != repository.DefaultPendingDeleteLimit {
		t.Errorf("default limit: got %d, want %d", f.listPendingCalls[0].limit, repository.DefaultPendingDeleteLimit)
	}
}

// TestListPending_clampsNegativeGrace verifies a negative grace_window_secs
// is clamped to 0 so an attacker can't pull "future" manifests into the
// candidate list.
func TestListPending_clampsNegativeGrace(t *testing.T) {
	f := &fakeRepo{}
	h := newHandler(f)
	_, err := h.ListPendingDeleteManifests(context.Background(), &metadatav1.ListPendingDeleteManifestsRequest{
		TenantId:        "t1",
		GraceWindowSecs: -1000,
	})
	requireNoErr(t, err)
	if f.listPendingCalls[0].graceWindowSecs != 0 {
		t.Errorf("grace clamp: got %d, want 0", f.listPendingCalls[0].graceWindowSecs)
	}
}

// TestListPending_returnsNonNilSlice verifies the wire shape always emits an
// array even when the repository returns nil (matches the rest of the
// handler family's wire-shape contract).
func TestListPending_returnsNonNilSlice(t *testing.T) {
	h := newHandler(&fakeRepo{listPendingResult: nil})
	resp, err := h.ListPendingDeleteManifests(context.Background(), &metadatav1.ListPendingDeleteManifestsRequest{
		TenantId: "t1",
	})
	requireNoErr(t, err)
	if resp.Manifests == nil {
		t.Error("manifests should be a non-nil slice")
	}
}

// TestListPending_forwardsTenantScope verifies an empty tenant_id flows through
// to the repository (cross-tenant scan) and a populated tenant_id stays
// populated.
func TestListPending_forwardsTenantScope(t *testing.T) {
	f := &fakeRepo{listPendingResult: []*metadatav1.PendingDeleteManifest{
		{ManifestId: "m1", PendingSince: timestamppb.New(time.Now().Add(-8 * 24 * time.Hour))},
	}}
	h := newHandler(f)
	_, err := h.ListPendingDeleteManifests(context.Background(), &metadatav1.ListPendingDeleteManifestsRequest{
		TenantId:        "",
		GraceWindowSecs: 7 * 24 * 3600,
	})
	requireNoErr(t, err)
	if f.listPendingCalls[0].tenantID != "" {
		t.Errorf("empty tenant should pass through, got %q", f.listPendingCalls[0].tenantID)
	}

	_, err = h.ListPendingDeleteManifests(context.Background(), &metadatav1.ListPendingDeleteManifestsRequest{
		TenantId:        "t1",
		GraceWindowSecs: 7 * 24 * 3600,
	})
	requireNoErr(t, err)
	if f.listPendingCalls[1].tenantID != "t1" {
		t.Errorf("tenant scoping mismatch: %+v", f.listPendingCalls[1])
	}
}

// Package service — unit tests for the FUT-021 CVSS-gated admission gate.
//
// These tests use a hand-written minimal MetadataServiceClient stub so we can
// pin the load-bearing invariants (fail-OPEN on no scan / scanner blip;
// fail-CLOSED on over-threshold; `>` not `>=`) without spinning up bufconn.
package service

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
)

// ── topCVSSFromSeverity table ─────────────────────────────────────────────────

// TestTopCVSSFromSeverity_tabledriven pins the band mapping. Threshold
// interpretation elsewhere depends on these specific midpoints — a change here
// silently reweights every existing CVSS admission policy, so any tweak should
// be a deliberate decision reflected in a migration + status.md note.
func TestTopCVSSFromSeverity_tabledriven(t *testing.T) {
	cases := []struct {
		name   string
		counts map[string]int32
		want   int32
	}{
		{"empty map", nil, 0},
		{"all zero", map[string]int32{"LOW": 0, "MEDIUM": 0, "HIGH": 0, "CRITICAL": 0}, 0},
		{"only LOW", map[string]int32{"LOW": 3}, 39},
		{"only MEDIUM", map[string]int32{"MEDIUM": 1}, 69},
		{"only HIGH", map[string]int32{"HIGH": 1}, 89},
		{"only CRITICAL", map[string]int32{"CRITICAL": 1}, 100},
		{"CRITICAL wins over HIGH", map[string]int32{"CRITICAL": 1, "HIGH": 5}, 100},
		{"HIGH wins over MEDIUM", map[string]int32{"HIGH": 1, "MEDIUM": 5}, 89},
		{"MEDIUM wins over LOW", map[string]int32{"MEDIUM": 1, "LOW": 99}, 69},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := topCVSSFromSeverity(tc.counts)
			if got != tc.want {
				t.Errorf("topCVSSFromSeverity: got %d, want %d", got, tc.want)
			}
		})
	}
}

// ── checkCVSSAdmission — six invariant scenarios ─────────────────────────────

// fakeCVSSMetadata is a stub MetadataServiceClient that exposes just the two
// RPCs checkCVSSAdmission calls: GetRepository and GetScanResult. Everything
// else short-circuits to Unimplemented — a wired test would fail loudly if the
// admission path ever grows a new RPC dependency.
type fakeCVSSMetadata struct {
	metadatav1.MetadataServiceClient

	repo    *metadatav1.Repository
	repoErr error

	scan    *metadatav1.ScanResult
	scanErr error

	// scanCalls counts GetScanResult invocations so tests can prove the
	// short-circuits skipped the scan lookup (e.g. NULL max_cvss_score).
	scanCalls int
}

func (f *fakeCVSSMetadata) GetRepository(_ context.Context, _ *metadatav1.GetRepositoryRequest, _ ...grpc.CallOption) (*metadatav1.Repository, error) {
	return f.repo, f.repoErr
}

func (f *fakeCVSSMetadata) GetScanResult(_ context.Context, _ *metadatav1.GetScanResultRequest, _ ...grpc.CallOption) (*metadatav1.ScanResult, error) {
	f.scanCalls++
	return f.scan, f.scanErr
}

// registryWithMetadata wires up a Registry with just a metadata client set.
// Every other field stays zero — checkCVSSAdmission does not touch storage,
// uploads, publisher, or signer.
func registryWithMetadata(m metadatav1.MetadataServiceClient) *Registry {
	return &Registry{metadata: m}
}

// TestCheckCVSSAdmission_NoPolicy_Allows — invariant 2. When
// max_cvss_score is null, the gate is fully disabled: no scan lookup
// happens and the pull is allowed unconditionally.
func TestCheckCVSSAdmission_NoPolicy_Allows(t *testing.T) {
	f := &fakeCVSSMetadata{
		repo: &metadatav1.Repository{RepoId: "r1", MaxCvssScore: nil},
	}
	r := registryWithMetadata(f)

	err := r.checkCVSSAdmission(context.Background(), "t1", "r1", "sha256:aa")
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if f.scanCalls != 0 {
		t.Errorf("expected scan lookup skipped, got %d calls", f.scanCalls)
	}
}

// TestCheckCVSSAdmission_NoScanResult_AllowsAndLogs — invariant 3.
// First-pull scenario: the repo has a threshold but the scanner has
// not produced a result yet. Fail-OPEN so operators don't block CI on
// scanner queue depth.
func TestCheckCVSSAdmission_NoScanResult_AllowsAndLogs(t *testing.T) {
	f := &fakeCVSSMetadata{
		repo:    &metadatav1.Repository{RepoId: "r1", MaxCvssScore: wrapperspb.Int32(70)},
		scanErr: status.Error(codes.NotFound, "no scan yet"),
	}
	r := registryWithMetadata(f)

	err := r.checkCVSSAdmission(context.Background(), "t1", "r1", "sha256:aa")
	if err != nil {
		t.Fatalf("expected fail-OPEN (nil err), got %v", err)
	}
	if f.scanCalls != 1 {
		t.Errorf("expected exactly 1 scan lookup, got %d", f.scanCalls)
	}
}

// TestCheckCVSSAdmission_ScannerUnreachable_AllowsAndWarns — invariant 4.
// Scanner / metadata blip: fail-OPEN to avoid a "gate goes down, every pull
// dies" cascade.
func TestCheckCVSSAdmission_ScannerUnreachable_AllowsAndWarns(t *testing.T) {
	f := &fakeCVSSMetadata{
		repo:    &metadatav1.Repository{RepoId: "r1", MaxCvssScore: wrapperspb.Int32(70)},
		scanErr: errors.New("bufconn closed"),
	}
	r := registryWithMetadata(f)

	err := r.checkCVSSAdmission(context.Background(), "t1", "r1", "sha256:aa")
	if err != nil {
		t.Fatalf("expected fail-OPEN (nil err), got %v", err)
	}
}

// TestCheckCVSSAdmission_UnderThreshold_Allows — invariant 5 (allow side).
// Threshold=70, top CVSS=39 (LOW only): well under, allow.
func TestCheckCVSSAdmission_UnderThreshold_Allows(t *testing.T) {
	f := &fakeCVSSMetadata{
		repo: &metadatav1.Repository{RepoId: "r1", MaxCvssScore: wrapperspb.Int32(70)},
		scan: &metadatav1.ScanResult{
			SeverityCounts: map[string]int32{"LOW": 5},
		},
	}
	r := registryWithMetadata(f)

	if err := r.checkCVSSAdmission(context.Background(), "t1", "r1", "sha256:aa"); err != nil {
		t.Fatalf("expected allow, got %v", err)
	}
}

// TestCheckCVSSAdmission_OverThreshold_Denies — invariant 5 (deny side).
// Threshold=70, top CVSS=100 (CRITICAL present): fail-CLOSED with the wrapped
// numeric context so the HTTP layer's error body is actionable.
func TestCheckCVSSAdmission_OverThreshold_Denies(t *testing.T) {
	f := &fakeCVSSMetadata{
		repo: &metadatav1.Repository{RepoId: "r1", MaxCvssScore: wrapperspb.Int32(70)},
		scan: &metadatav1.ScanResult{
			SeverityCounts: map[string]int32{"CRITICAL": 1},
		},
	}
	r := registryWithMetadata(f)

	err := r.checkCVSSAdmission(context.Background(), "t1", "r1", "sha256:aa")
	if err == nil {
		t.Fatal("expected ErrCVSSThresholdExceeded, got nil")
	}
	if !errors.Is(err, ErrCVSSThresholdExceeded) {
		t.Errorf("expected errors.Is ErrCVSSThresholdExceeded, got %v", err)
	}
	// The HTTP layer surfaces err.Error() verbatim; keep the numeric context
	// in the message so CI tooling can parse it.
	msg := err.Error()
	if !containsAll(msg, "100", "70") {
		t.Errorf("expected err message to include top(100) + threshold(70), got %q", msg)
	}
}

// TestCheckCVSSAdmission_ExactlyAtThreshold_Allows — invariant 6.
// Threshold=89, top CVSS=89 (HIGH). Comparison is strict `>` not `>=`, so a
// score exactly at the threshold is allowed. Load-bearing so operators can
// pin a threshold to "block anything worse than HIGH" without losing HIGH.
func TestCheckCVSSAdmission_ExactlyAtThreshold_Allows(t *testing.T) {
	f := &fakeCVSSMetadata{
		repo: &metadatav1.Repository{RepoId: "r1", MaxCvssScore: wrapperspb.Int32(89)},
		scan: &metadatav1.ScanResult{
			SeverityCounts: map[string]int32{"HIGH": 1},
		},
	}
	r := registryWithMetadata(f)

	if err := r.checkCVSSAdmission(context.Background(), "t1", "r1", "sha256:aa"); err != nil {
		t.Fatalf("expected allow at exactly threshold, got %v", err)
	}
}

// TestCheckCVSSAdmission_RepoLookupFails_Allows — invariant 1. Metadata blip
// on the repo lookup itself. Fail-OPEN so a transient outage doesn't cascade
// into a registry-wide pull outage.
func TestCheckCVSSAdmission_RepoLookupFails_Allows(t *testing.T) {
	f := &fakeCVSSMetadata{
		repoErr: errors.New("bufconn closed"),
	}
	r := registryWithMetadata(f)

	if err := r.checkCVSSAdmission(context.Background(), "t1", "r1", "sha256:aa"); err != nil {
		t.Fatalf("expected fail-OPEN on repo blip, got %v", err)
	}
	if f.scanCalls != 0 {
		t.Errorf("expected scan lookup skipped when repo failed, got %d calls", f.scanCalls)
	}
}

// TestCheckCVSSAdmission_MediumBlockedByLowerThreshold verifies a common
// operator posture: threshold=50 (below MEDIUM band midpoint 69) blocks any
// scan carrying a MEDIUM finding. Exercised because the band mapping is
// load-bearing for policy semantics.
func TestCheckCVSSAdmission_MediumBlockedByLowerThreshold(t *testing.T) {
	f := &fakeCVSSMetadata{
		repo: &metadatav1.Repository{RepoId: "r1", MaxCvssScore: wrapperspb.Int32(50)},
		scan: &metadatav1.ScanResult{
			SeverityCounts: map[string]int32{"MEDIUM": 1},
		},
	}
	r := registryWithMetadata(f)

	err := r.checkCVSSAdmission(context.Background(), "t1", "r1", "sha256:aa")
	if !errors.Is(err, ErrCVSSThresholdExceeded) {
		t.Fatalf("expected deny (MEDIUM=69 > 50), got %v", err)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// containsAll is a tiny helper that avoids pulling in strings/regexp for a
// substring-any check in the wrapped-error assertion above.
func containsAll(haystack string, needles ...string) bool {
	for _, n := range needles {
		if !containsSub(haystack, n) {
			return false
		}
	}
	return true
}

func containsSub(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// ensure emptypb stays referenced for the fake stub imports even if the
// interface embed evolves.
var _ = emptypb.Empty{}

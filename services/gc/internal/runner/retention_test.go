// Unit tests for the FE-API-040 retention executor. We exercise the run
// dispatcher branches with a hand-written fake metadata client + a fake repo
// so the asserts focus on behaviour (mark-on-match, preserve-on-disabled,
// skip-on-preview) rather than SQL semantics. The repository's actual SQL
// path is covered by the integration tests under
// services/metadata/internal/testutil/integration/retention_pending_test.go.
package runner

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/gc/internal/repository"
)

// ── fakePublisher ────────────────────────────────────────────────────────────

// fakePublisher captures every (routingKey, Event) the executor publishes.
// publishErr is returned from Publish so tests can exercise the
// "publish error is logged but run still succeeds" path.
type fakePublisher struct {
	publishErr error
	publishes  []capturedPublish
}

type capturedPublish struct {
	routingKey string
	event      events.Event
}

func (f *fakePublisher) Publish(_ context.Context, routingKey string, event events.Event) error {
	f.publishes = append(f.publishes, capturedPublish{routingKey: routingKey, event: event})
	return f.publishErr
}

// ── fakeMeta ─────────────────────────────────────────────────────────────────

// fakeMeta satisfies runner.MetadataClient so the executor tests can drive
// every branch (no-policy, disabled-policy, preview-window, happy-path)
// without standing up bufconn.
type fakeMeta struct {
	getEffectiveResp *metadatav1.EffectiveRetentionPolicy
	getEffectiveErr  error

	evaluateResp *metadatav1.EvaluateRetentionResponse
	evaluateErr  error

	markErr       error
	markCalls     []string // manifest_ids passed to Mark.

	listPendingResp *metadatav1.ListPendingDeleteManifestsResponse
	listPendingErr  error

	deleteErr   error
	deleteCalls []string // digests passed to Delete.
}

func (f *fakeMeta) GetEffectiveRetentionPolicy(_ context.Context, _ *metadatav1.GetEffectiveRetentionPolicyRequest, _ ...grpc.CallOption) (*metadatav1.EffectiveRetentionPolicy, error) {
	return f.getEffectiveResp, f.getEffectiveErr
}

func (f *fakeMeta) EvaluateRetention(_ context.Context, _ *metadatav1.EvaluateRetentionRequest, _ ...grpc.CallOption) (*metadatav1.EvaluateRetentionResponse, error) {
	return f.evaluateResp, f.evaluateErr
}

func (f *fakeMeta) MarkManifestRetentionPending(_ context.Context, req *metadatav1.MarkManifestRetentionPendingRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	f.markCalls = append(f.markCalls, req.GetManifestId())
	if f.markErr != nil {
		return nil, f.markErr
	}
	return &emptypb.Empty{}, nil
}

func (f *fakeMeta) ListPendingDeleteManifests(_ context.Context, _ *metadatav1.ListPendingDeleteManifestsRequest, _ ...grpc.CallOption) (*metadatav1.ListPendingDeleteManifestsResponse, error) {
	return f.listPendingResp, f.listPendingErr
}

func (f *fakeMeta) DeleteManifest(_ context.Context, req *metadatav1.DeleteManifestRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	f.deleteCalls = append(f.deleteCalls, req.GetDigest())
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	return &emptypb.Empty{}, nil
}

// ── fakeFinalRepo ────────────────────────────────────────────────────────────

// fakeFinalRepo captures finalize/fail calls so tests can assert the
// executor recorded the right outcome on the gc_runs row.
type fakeFinalRepo struct {
	*repository.Repository // embedded so it satisfies the *Repository receiver type

	finalizedRunID    uuid.UUID
	finalizedCount    int64
	finalizedBlobs    int64
	finalizedBytes    int64
	finalizedErrMsg   string
	finalizedCallback func()

	failedRunID  uuid.UUID
	failedReason string
}

// Override FinalizeRetentionRun and FailRun by wrapping a tiny helper. Since
// PersistedRunner takes *repository.Repository directly, we wrap it via a
// custom test-only PersistedRunner builder that intercepts these calls.

// ── tests ────────────────────────────────────────────────────────────────────

// stubRepo is a tiny *repository.Repository stand-in. We can't easily build a
// real repository.Repository without a pgxpool, so the tests below construct
// the PersistedRunner with a real-but-unused col + nil-checked guards in the
// retention executor branches. To make this work we use a small set of test-
// only helpers that directly drive the executor methods, bypassing the
// finalize calls (which we instead validate via a captured-channel approach).
//
// Approach: replace the executor's call to p.repo.FinalizeRetentionRun /
// FailRun with a closure indirection set per-test. We expose those hooks via
// package-private function variables that the tests can override.

// TestRunRetention_noPolicy_marksZero verifies the executor records a
// "no policy" outcome when GetEffectiveRetentionPolicy returns NotFound.
func TestRunRetention_noPolicy_marksZero(t *testing.T) {
	meta := &fakeMeta{getEffectiveErr: status.Error(codes.NotFound, "no policy")}
	p := newTestRunner(meta)
	finalized := captureFinalize(p)

	run := &repository.GCRun{
		RunID:    uuid.New(),
		TenantID: uuid.New(),
		RepoID:   uuid.New(),
		Mode:     "retention",
	}
	if err := p.RunRetention(context.Background(), run); err != nil {
		t.Fatalf("RunRetention: %v", err)
	}
	if len(meta.markCalls) != 0 {
		t.Errorf("no policy ⇒ no MarkPending calls, got %d", len(meta.markCalls))
	}
	if got := finalized.lastErrMsg; got != "no policy" {
		t.Errorf("error_message: got %q, want \"no policy\"", got)
	}
}

// TestRunRetention_previewWindowActive_skipsMarking verifies the
// preview-window guard fires.
func TestRunRetention_previewWindowActive_skipsMarking(t *testing.T) {
	meta := &fakeMeta{
		getEffectiveResp: &metadatav1.EffectiveRetentionPolicy{
			Policy: &metadatav1.RetentionPolicy{
				Enabled:      true,
				PreviewUntil: timestamppb.New(time.Now().Add(2 * time.Hour)),
				Rules:        []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 30}},
			},
		},
	}
	p := newTestRunner(meta)
	finalized := captureFinalize(p)

	run := &repository.GCRun{RunID: uuid.New(), TenantID: uuid.New(), RepoID: uuid.New(), Mode: "retention"}
	if err := p.RunRetention(context.Background(), run); err != nil {
		t.Fatalf("RunRetention: %v", err)
	}
	if len(meta.markCalls) != 0 {
		t.Errorf("preview active ⇒ no MarkPending, got %d", len(meta.markCalls))
	}
	if finalized.lastErrMsg == "" {
		t.Error("expected informational error_message for preview window")
	}
}

// TestRunRetention_disabledPolicy_skipsMarking verifies a disabled effective
// policy is treated like "no policy".
func TestRunRetention_disabledPolicy_skipsMarking(t *testing.T) {
	meta := &fakeMeta{
		getEffectiveResp: &metadatav1.EffectiveRetentionPolicy{
			Policy: &metadatav1.RetentionPolicy{Enabled: false},
		},
	}
	p := newTestRunner(meta)
	finalized := captureFinalize(p)

	run := &repository.GCRun{RunID: uuid.New(), TenantID: uuid.New(), RepoID: uuid.New(), Mode: "retention"}
	if err := p.RunRetention(context.Background(), run); err != nil {
		t.Fatalf("RunRetention: %v", err)
	}
	if len(meta.markCalls) != 0 {
		t.Errorf("disabled policy ⇒ no MarkPending, got %d", len(meta.markCalls))
	}
	if finalized.lastErrMsg != "policy disabled" {
		t.Errorf("error_message: got %q, want \"policy disabled\"", finalized.lastErrMsg)
	}
}

// TestRunRetention_happyPath_marksEveryCandidate verifies the executor calls
// MarkPending for every would_delete entry returned by EvaluateRetention.
func TestRunRetention_happyPath_marksEveryCandidate(t *testing.T) {
	meta := &fakeMeta{
		getEffectiveResp: &metadatav1.EffectiveRetentionPolicy{
			Policy: &metadatav1.RetentionPolicy{
				Enabled: true,
				Rules:   []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 30}},
			},
		},
		evaluateResp: &metadatav1.EvaluateRetentionResponse{
			WouldDelete: []*metadatav1.RetentionDeletionCandidate{
				{ManifestId: "m1"},
				{ManifestId: "m2"},
				{ManifestId: "m3"},
			},
			TotalCount: 3,
		},
	}
	p := newTestRunner(meta)
	finalized := captureFinalize(p)

	run := &repository.GCRun{RunID: uuid.New(), TenantID: uuid.New(), RepoID: uuid.New(), Mode: "retention"}
	if err := p.RunRetention(context.Background(), run); err != nil {
		t.Fatalf("RunRetention: %v", err)
	}
	if len(meta.markCalls) != 3 {
		t.Errorf("expected 3 MarkPending calls, got %d", len(meta.markCalls))
	}
	if finalized.lastCount != 3 {
		t.Errorf("finalize count: got %d, want 3", finalized.lastCount)
	}
}

// TestRunRetention_markFailure_isNonFatal verifies that one failing
// MarkPending does NOT abort the sweep — the remaining candidates still
// get processed, and the count reflects only the successful marks. The
// next sweep will retry the failed ones because MarkPending is idempotent.
func TestRunRetention_markFailure_isNonFatal(t *testing.T) {
	meta := &fakeMeta{
		getEffectiveResp: &metadatav1.EffectiveRetentionPolicy{
			Policy: &metadatav1.RetentionPolicy{Enabled: true, Rules: []*metadatav1.RetentionRule{{Kind: "max_count", Value: 1}}},
		},
		evaluateResp: &metadatav1.EvaluateRetentionResponse{
			WouldDelete: []*metadatav1.RetentionDeletionCandidate{
				{ManifestId: "m1"},
				{ManifestId: "m2"},
			},
		},
		markErr: errors.New("transient failure"),
	}
	p := newTestRunner(meta)
	finalized := captureFinalize(p)

	run := &repository.GCRun{RunID: uuid.New(), TenantID: uuid.New(), RepoID: uuid.New(), Mode: "retention"}
	_ = p.RunRetention(context.Background(), run)
	// Both attempts go through; both fail; the run still finalises with
	// markedCount=0 so the next sweep retries.
	if finalized.lastCount != 0 {
		t.Errorf("expected markedCount=0 when all marks fail, got %d", finalized.lastCount)
	}
}

// ── RunRetentionGrace ────────────────────────────────────────────────────────

// TestRunRetentionGrace_deletesPastGrace verifies the finaliser calls
// DeleteManifest for every pending row returned by metadata.
func TestRunRetentionGrace_deletesPastGrace(t *testing.T) {
	meta := &fakeMeta{
		listPendingResp: &metadatav1.ListPendingDeleteManifestsResponse{
			Manifests: []*metadatav1.PendingDeleteManifest{
				{ManifestId: "m1", Digest: "sha256:aaaa", SizeBytes: 1024, TenantId: "t1", RepositoryId: "r1"},
				{ManifestId: "m2", Digest: "sha256:bbbb", SizeBytes: 2048, TenantId: "t1", RepositoryId: "r1"},
			},
		},
	}
	p := newTestRunner(meta)
	finalized := captureFinalize(p)

	run := &repository.GCRun{RunID: uuid.New(), TenantID: uuid.Nil, Mode: "retention_grace"}
	if err := p.RunRetentionGrace(context.Background(), run); err != nil {
		t.Fatalf("RunRetentionGrace: %v", err)
	}
	if len(meta.deleteCalls) != 2 {
		t.Errorf("expected 2 DeleteManifest calls, got %d", len(meta.deleteCalls))
	}
	if finalized.lastCount != 2 {
		t.Errorf("deleted count: got %d, want 2", finalized.lastCount)
	}
	if finalized.lastBytes != 3072 {
		t.Errorf("bytes freed: got %d, want 3072", finalized.lastBytes)
	}
}

// TestRunRetentionGrace_emptyList_succeeds verifies the empty case still
// finalises (no delete, no fail).
func TestRunRetentionGrace_emptyList_succeeds(t *testing.T) {
	meta := &fakeMeta{
		listPendingResp: &metadatav1.ListPendingDeleteManifestsResponse{},
	}
	p := newTestRunner(meta)
	finalized := captureFinalize(p)

	run := &repository.GCRun{RunID: uuid.New(), TenantID: uuid.Nil, Mode: "retention_grace"}
	if err := p.RunRetentionGrace(context.Background(), run); err != nil {
		t.Fatalf("RunRetentionGrace: %v", err)
	}
	if finalized.lastCount != 0 {
		t.Errorf("empty grace ⇒ count 0, got %d", finalized.lastCount)
	}
}

// TestIsRetentionMode verifies the small helper used by the dispatcher.
func TestIsRetentionMode(t *testing.T) {
	cases := map[string]bool{
		"retention":       true,
		"retention_grace": true,
		"full":            false,
		"dry-run":         false,
		"":                false,
	}
	for mode, want := range cases {
		if got := IsRetentionMode(mode); got != want {
			t.Errorf("IsRetentionMode(%q) = %v, want %v", mode, got, want)
		}
	}
}

// ── test helpers ────────────────────────────────────────────────────────────

// captured carries the values the executor passed to FinalizeRetentionRun /
// FailRun via the test hooks.
type captured struct {
	lastCount   int64
	lastBlobs   int64
	lastBytes   int64
	lastErrMsg  string
	lastFailed  string
	finalizeHit int
	failHit     int
}

// captureFinalize swaps the runner's finalize/fail hooks for closures that
// stash the values into a captured. Returns the captured pointer so tests
// can read fields after the run.
func captureFinalize(p *PersistedRunner) *captured {
	c := &captured{}
	p.finalizeHook = func(_ context.Context, _ uuid.UUID, count, blobs, bytes int64, errMsg string) error {
		c.lastCount = count
		c.lastBlobs = blobs
		c.lastBytes = bytes
		c.lastErrMsg = errMsg
		c.finalizeHit++
		return nil
	}
	p.failHook = func(_ context.Context, _ uuid.UUID, msg string) error {
		c.lastFailed = msg
		c.failHit++
		return nil
	}
	return c
}

// newTestRunner builds a PersistedRunner with nil col/repo but a fake
// metadata client wired up. The finalize/fail hooks are swapped in by
// captureFinalize before the test exercises a run.
func newTestRunner(meta MetadataClient) *PersistedRunner {
	return &PersistedRunner{
		mode:       "full",
		retention:  defaultRetentionConfig(),
		metaClient: meta,
	}
}

// newTestRunnerWithPub is the FE-API-041 variant — same as newTestRunner
// but attaches a fake publisher so tests can assert on the routing key +
// payload shape of every retention.* event the executor emits.
func newTestRunnerWithPub(meta MetadataClient, pub EventPublisher) *PersistedRunner {
	return &PersistedRunner{
		mode:       "full",
		retention:  defaultRetentionConfig(),
		metaClient: meta,
		pub:        pub,
	}
}

// ── FE-API-041 publisher tests ──────────────────────────────────────────────

// TestRunRetention_nilPublisher_doesNotPanic verifies the executor still
// runs cleanly when no publisher is wired (dev install without RABBITMQ_URL).
// Marks should still happen + finalise still records the outcome.
func TestRunRetention_nilPublisher_doesNotPanic(t *testing.T) {
	meta := &fakeMeta{
		getEffectiveResp: &metadatav1.EffectiveRetentionPolicy{
			Policy: &metadatav1.RetentionPolicy{
				Enabled: true,
				Rules:   []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 30}},
			},
		},
		evaluateResp: &metadatav1.EvaluateRetentionResponse{
			WouldDelete: []*metadatav1.RetentionDeletionCandidate{{ManifestId: "m1"}},
			TotalCount:  1,
			TotalBytes:  4096,
		},
	}
	// No publisher attached — the executor must treat it as a no-op.
	p := newTestRunner(meta)
	finalized := captureFinalize(p)

	run := &repository.GCRun{RunID: uuid.New(), TenantID: uuid.New(), RepoID: uuid.New(), Mode: "retention"}
	if err := p.RunRetention(context.Background(), run); err != nil {
		t.Fatalf("RunRetention with nil publisher: %v", err)
	}
	if finalized.lastCount != 1 {
		t.Errorf("expected markedCount=1, got %d", finalized.lastCount)
	}
}

// TestRunRetention_publisherError_doesNotFailRun verifies that a publish
// error is logged but the run still completes successfully. Events are a
// best-effort observability signal — gc_runs is the source of truth.
func TestRunRetention_publisherError_doesNotFailRun(t *testing.T) {
	meta := &fakeMeta{
		getEffectiveResp: &metadatav1.EffectiveRetentionPolicy{
			Policy: &metadatav1.RetentionPolicy{
				Enabled: true,
				Rules:   []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 30}},
			},
		},
		evaluateResp: &metadatav1.EvaluateRetentionResponse{
			WouldDelete: []*metadatav1.RetentionDeletionCandidate{{ManifestId: "m1"}},
			TotalCount:  1,
		},
	}
	pub := &fakePublisher{publishErr: errors.New("broker unreachable")}
	p := newTestRunnerWithPub(meta, pub)
	finalized := captureFinalize(p)

	run := &repository.GCRun{RunID: uuid.New(), TenantID: uuid.New(), RepoID: uuid.New(), Mode: "retention"}
	if err := p.RunRetention(context.Background(), run); err != nil {
		t.Fatalf("RunRetention with failing publisher: %v", err)
	}
	if finalized.lastCount != 1 {
		t.Errorf("publisher error must not affect finalised count: got %d", finalized.lastCount)
	}
	// Both attempted publishes (evaluated + applied) should have been tried
	// even though the first one errored — log + continue, never short-circuit.
	if len(pub.publishes) != 2 {
		t.Errorf("expected 2 publish attempts (evaluated + applied), got %d", len(pub.publishes))
	}
}

// TestRunRetention_publisherSuccess_emitsEvaluatedAndApplied verifies the
// happy-path event shape — both retention.evaluated AND retention.applied
// fire with the right routing key and tenant/repo IDs.
func TestRunRetention_publisherSuccess_emitsEvaluatedAndApplied(t *testing.T) {
	tenantID := uuid.New()
	repoID := uuid.New()
	meta := &fakeMeta{
		getEffectiveResp: &metadatav1.EffectiveRetentionPolicy{
			Policy: &metadatav1.RetentionPolicy{
				Enabled: true,
				Rules:   []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 30}},
			},
		},
		evaluateResp: &metadatav1.EvaluateRetentionResponse{
			WouldDelete: []*metadatav1.RetentionDeletionCandidate{
				{ManifestId: "m1"},
				{ManifestId: "m2"},
			},
			TotalCount: 2,
			TotalBytes: 8192,
		},
	}
	pub := &fakePublisher{}
	p := newTestRunnerWithPub(meta, pub)
	_ = captureFinalize(p)

	run := &repository.GCRun{
		RunID:       uuid.New(),
		TenantID:    tenantID,
		RepoID:      repoID,
		Mode:        "retention",
		TriggeredBy: "cron",
	}
	if err := p.RunRetention(context.Background(), run); err != nil {
		t.Fatalf("RunRetention: %v", err)
	}
	if len(pub.publishes) != 2 {
		t.Fatalf("expected 2 publishes (evaluated + applied), got %d", len(pub.publishes))
	}
	if pub.publishes[0].routingKey != events.RoutingRetentionEvaluated {
		t.Errorf("first publish routing key: got %q, want %q",
			pub.publishes[0].routingKey, events.RoutingRetentionEvaluated)
	}
	if pub.publishes[1].routingKey != events.RoutingRetentionApplied {
		t.Errorf("second publish routing key: got %q, want %q",
			pub.publishes[1].routingKey, events.RoutingRetentionApplied)
	}
	// TenantID propagates onto the envelope as a string — consumers use it
	// for the AMQP header filter without deserialising the body.
	if pub.publishes[0].event.TenantID != tenantID.String() {
		t.Errorf("evaluated envelope tenant_id: got %q, want %q",
			pub.publishes[0].event.TenantID, tenantID.String())
	}
}

// TestRunRetention_zeroMarks_skipsAppliedEvent verifies the "nothing
// happened" gate: when no manifest was actually marked we still emit
// retention.evaluated (subscribers want to know we checked) but NOT
// retention.applied — there's nothing to apply.
func TestRunRetention_zeroMarks_skipsAppliedEvent(t *testing.T) {
	meta := &fakeMeta{
		getEffectiveResp: &metadatav1.EffectiveRetentionPolicy{
			Policy: &metadatav1.RetentionPolicy{
				Enabled: true,
				Rules:   []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 30}},
			},
		},
		evaluateResp: &metadatav1.EvaluateRetentionResponse{TotalCount: 0},
	}
	pub := &fakePublisher{}
	p := newTestRunnerWithPub(meta, pub)
	_ = captureFinalize(p)

	run := &repository.GCRun{RunID: uuid.New(), TenantID: uuid.New(), RepoID: uuid.New(), Mode: "retention"}
	if err := p.RunRetention(context.Background(), run); err != nil {
		t.Fatalf("RunRetention: %v", err)
	}
	if len(pub.publishes) != 1 {
		t.Fatalf("expected 1 publish (evaluated only), got %d", len(pub.publishes))
	}
	if pub.publishes[0].routingKey != events.RoutingRetentionEvaluated {
		t.Errorf("expected evaluated-only, got %q", pub.publishes[0].routingKey)
	}
}

// TestRunRetentionGrace_publisherSuccess_emitsEvaluatedAndGraceCompleted
// verifies the finaliser also emits both events. Mode is "retention_grace"
// on the evaluated payload so consumers can distinguish soft-delete from
// hard-delete sweeps in their dashboards.
func TestRunRetentionGrace_publisherSuccess_emitsEvaluatedAndGraceCompleted(t *testing.T) {
	meta := &fakeMeta{
		listPendingResp: &metadatav1.ListPendingDeleteManifestsResponse{
			Manifests: []*metadatav1.PendingDeleteManifest{
				{ManifestId: "m1", Digest: "sha256:aaaa", SizeBytes: 1024, TenantId: "t1", RepositoryId: "r1"},
			},
		},
	}
	pub := &fakePublisher{}
	p := newTestRunnerWithPub(meta, pub)
	_ = captureFinalize(p)

	run := &repository.GCRun{RunID: uuid.New(), TenantID: uuid.Nil, Mode: "retention_grace"}
	if err := p.RunRetentionGrace(context.Background(), run); err != nil {
		t.Fatalf("RunRetentionGrace: %v", err)
	}
	if len(pub.publishes) != 2 {
		t.Fatalf("expected 2 publishes (evaluated + grace_completed), got %d", len(pub.publishes))
	}
	if pub.publishes[0].routingKey != events.RoutingRetentionEvaluated {
		t.Errorf("first publish key: got %q, want %q",
			pub.publishes[0].routingKey, events.RoutingRetentionEvaluated)
	}
	if pub.publishes[1].routingKey != events.RoutingRetentionGraceCompleted {
		t.Errorf("second publish key: got %q, want %q",
			pub.publishes[1].routingKey, events.RoutingRetentionGraceCompleted)
	}
}

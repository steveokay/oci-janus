package prregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
)

const testSecret = "hook-secret"

// baseCfg returns an enabled config sealed with testKEK, optionally with a
// promote target.
func baseCfg(t *testing.T, tenantID uuid.UUID, promoteTarget string) (repository.PRRegistryConfig, []byte) {
	t.Helper()
	kek := testKEK()
	return repository.PRRegistryConfig{
		TenantID:         tenantID,
		Enabled:          true,
		WebhookSecretEnc: sealSecret(t, testSecret, kek),
		PromoteTargetOrg: promoteTarget,
	}, kek
}

// prBody builds a signed pull_request payload + its X-Hub-Signature-256.
func prBody(action string, number int, merged bool, repoName string) []byte {
	full := "acme/" + repoName
	return []byte(fmt.Sprintf(
		`{"action":%q,"number":%d,"pull_request":{"merged":%t},"repository":{"full_name":%q,"name":%q}}`,
		action, number, merged, full, repoName))
}

// --- provider / event / signature gates ------------------------------------

func TestHandleEvent_NonGitHubProvider(t *testing.T) {
	tenantID := uuid.New()
	cfg, kek := baseCfg(t, tenantID, "")
	s := New(newFakeStore(), &fakePublisher{}, kek)
	out, _, err := s.HandleEvent(context.Background(), cfg, "gitlab", []byte(`{}`), "", "pull_request")
	if err != nil || out != OutcomeIgnored {
		t.Fatalf("got (%v,%v), want (IGNORED,nil)", out, err)
	}
}

func TestHandleEvent_PingIgnored(t *testing.T) {
	tenantID := uuid.New()
	cfg, kek := baseCfg(t, tenantID, "")
	s := New(newFakeStore(), &fakePublisher{}, kek)
	out, _, err := s.HandleEvent(context.Background(), cfg, "github", []byte(`{"zen":"x"}`), "", "ping")
	if err != nil || out != OutcomeIgnored {
		t.Fatalf("got (%v,%v), want (IGNORED,nil)", out, err)
	}
}

func TestHandleEvent_Disabled(t *testing.T) {
	tenantID := uuid.New()
	cfg, kek := baseCfg(t, tenantID, "")
	cfg.Enabled = false
	body := prBody("opened", 1, false, "svc")
	s := New(newFakeStore(), &fakePublisher{}, kek)
	out, org, err := s.HandleEvent(context.Background(), cfg, "github", body, signGitHub(testSecret, body), "pull_request")
	if err != nil || out != OutcomeDisabled || org != "" {
		t.Fatalf("got (%v,%q,%v), want (DISABLED,\"\",nil)", out, org, err)
	}
}

func TestHandleEvent_BadSignature(t *testing.T) {
	tenantID := uuid.New()
	cfg, kek := baseCfg(t, tenantID, "")
	body := prBody("opened", 1, false, "svc")
	store := newFakeStore()
	s := New(store, &fakePublisher{}, kek)
	out, _, err := s.HandleEvent(context.Background(), cfg, "github", body, "sha256=deadbeef", "pull_request")
	if !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("err = %v, want ErrSignatureMismatch", err)
	}
	if out != OutcomeIgnored {
		t.Fatalf("out = %v, want IGNORED", out)
	}
	// Body must never be acted on when the signature is bad.
	if len(store.upsertCalls) != 0 {
		t.Fatalf("upsert happened despite bad signature")
	}
}

// --- provision (opened / reopened) -----------------------------------------

func TestHandleEvent_OpenedProvisions(t *testing.T) {
	tenantID := uuid.New()
	cfg, kek := baseCfg(t, tenantID, "")
	body := prBody("opened", 42, false, "backend")
	store := newFakeStore()
	pub := &fakePublisher{}
	s := New(store, pub, kek)

	out, org, err := s.HandleEvent(context.Background(), cfg, "github", body, signGitHub(testSecret, body), "pull_request")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != OutcomeProvisioned {
		t.Fatalf("out = %v, want PROVISIONED", out)
	}
	if org != "pr-backend-42" {
		t.Fatalf("org = %q, want pr-backend-42", org)
	}
	if len(store.upsertCalls) != 1 {
		t.Fatalf("upsert calls = %d, want 1", len(store.upsertCalls))
	}
	// Exactly one provisioned event with the right payload.
	if len(pub.published) != 1 || pub.published[0].routingKey != events.RoutingPRNamespaceProvisioned {
		t.Fatalf("published = %+v, want one provisioned event", pub.published)
	}
	var p events.PRNamespaceProvisionedPayload
	if err := json.Unmarshal(pub.published[0].evt.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.OrgName != "pr-backend-42" || p.PRNumber != 42 || p.Provider != "github" || p.SourceRepo != "acme/backend" {
		t.Fatalf("payload = %+v", p)
	}
}

func TestHandleEvent_DoubleOpenedIdempotent(t *testing.T) {
	tenantID := uuid.New()
	cfg, kek := baseCfg(t, tenantID, "")
	body := prBody("reopened", 5, false, "svc")
	store := newFakeStore()
	pub := &fakePublisher{}
	s := New(store, pub, kek)

	for i := 0; i < 2; i++ {
		out, _, err := s.HandleEvent(context.Background(), cfg, "github", body, signGitHub(testSecret, body), "pull_request")
		if err != nil || out != OutcomeProvisioned {
			t.Fatalf("call %d got (%v,%v)", i, out, err)
		}
	}
	// Upsert (idempotent) called twice, but the namespace map holds one row.
	if len(store.upsertCalls) != 2 {
		t.Fatalf("upsert calls = %d, want 2", len(store.upsertCalls))
	}
	if len(store.namespaces) != 1 {
		t.Fatalf("namespaces = %d, want 1", len(store.namespaces))
	}
}

// --- teardown (closed, unmerged) -------------------------------------------

func TestHandleEvent_ClosedUnmergedTearsDown(t *testing.T) {
	tenantID := uuid.New()
	cfg, kek := baseCfg(t, tenantID, "")
	store := newFakeStore()
	store.seedNamespace(tenantID, "github", "acme/svc", 3, "pr-svc-3")
	pub := &fakePublisher{}
	s := New(store, pub, kek)

	body := prBody("closed", 3, false, "svc")
	out, org, err := s.HandleEvent(context.Background(), cfg, "github", body, signGitHub(testSecret, body), "pull_request")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != OutcomeTornDown || org != "pr-svc-3" {
		t.Fatalf("got (%v,%q), want (TORN_DOWN,pr-svc-3)", out, org)
	}
	if store.tearDownCalls != 1 {
		t.Fatalf("teardown calls = %d, want 1", store.tearDownCalls)
	}
	if len(pub.published) != 1 || pub.published[0].routingKey != events.RoutingPRNamespaceTornDown {
		t.Fatalf("published = %+v", pub.published)
	}
	var p events.PRNamespaceTornDownPayload
	_ = json.Unmarshal(pub.published[0].evt.Payload, &p)
	if p.Promoted != false || p.TargetOrg != "" {
		t.Fatalf("payload = %+v, want promoted=false no target", p)
	}
}

func TestHandleEvent_ClosedNeverProvisionedNoOp(t *testing.T) {
	tenantID := uuid.New()
	cfg, kek := baseCfg(t, tenantID, "")
	store := newFakeStore() // no seeded namespace
	pub := &fakePublisher{}
	s := New(store, pub, kek)

	body := prBody("closed", 99, false, "ghost")
	out, _, err := s.HandleEvent(context.Background(), cfg, "github", body, signGitHub(testSecret, body), "pull_request")
	if err != nil || out != OutcomeTornDown {
		t.Fatalf("got (%v,%v), want (TORN_DOWN,nil)", out, err)
	}
	if store.tearDownCalls != 0 {
		t.Fatalf("teardown should not be called for a missing namespace")
	}
	if len(pub.published) != 0 {
		t.Fatalf("no event should publish for a no-op teardown")
	}
}

func TestHandleEvent_DoubleTeardownIdempotent(t *testing.T) {
	tenantID := uuid.New()
	cfg, kek := baseCfg(t, tenantID, "")
	store := newFakeStore()
	store.seedNamespace(tenantID, "github", "acme/svc", 3, "pr-svc-3")
	pub := &fakePublisher{}
	s := New(store, pub, kek)

	body := prBody("closed", 3, false, "svc")
	sig := signGitHub(testSecret, body)
	for i := 0; i < 2; i++ {
		out, _, err := s.HandleEvent(context.Background(), cfg, "github", body, sig, "pull_request")
		if err != nil || out != OutcomeTornDown {
			t.Fatalf("call %d got (%v,%v)", i, out, err)
		}
	}
	// Only the first close actually tears down; the second sees status
	// torn_down and no-ops.
	if store.tearDownCalls != 1 {
		t.Fatalf("teardown calls = %d, want 1 (idempotent)", store.tearDownCalls)
	}
	if len(pub.published) != 1 {
		t.Fatalf("published = %d events, want 1 (no re-publish on repeat)", len(pub.published))
	}
}

// --- synchronize / unhandled actions ---------------------------------------

func TestHandleEvent_SynchronizeIgnored(t *testing.T) {
	tenantID := uuid.New()
	cfg, kek := baseCfg(t, tenantID, "")
	store := newFakeStore()
	s := New(store, &fakePublisher{}, kek)
	body := prBody("synchronize", 1, false, "svc")
	out, _, err := s.HandleEvent(context.Background(), cfg, "github", body, signGitHub(testSecret, body), "pull_request")
	if err != nil || out != OutcomeIgnored {
		t.Fatalf("got (%v,%v), want (IGNORED,nil)", out, err)
	}
	if store.tearDownCalls != 0 || len(store.upsertCalls) != 0 {
		t.Fatalf("synchronize must not mutate state")
	}
}

// --- promote-on-merge ------------------------------------------------------

// seedRepoTags wires a namespace's org with repositories + tags for the
// promote fan-out.
func seedRepoTags(store *fakeStore, orgID string, repos map[string][]string) {
	for repoName, tags := range repos {
		repoID := uuid.NewSHA1(uuid.Nil, []byte(orgID+"/"+repoName)).String()
		store.reposByOrg[orgID] = append(store.reposByOrg[orgID], &metadatav1.Repository{
			RepoId: repoID,
			Name:   repoName,
		})
		var ts []*metadatav1.Tag
		for _, tg := range tags {
			ts = append(ts, &metadatav1.Tag{Name: tg})
		}
		store.tagsByRepo[repoID] = ts
	}
}

func TestHandleEvent_MergedNoTargetJustTearsDown(t *testing.T) {
	tenantID := uuid.New()
	cfg, kek := baseCfg(t, tenantID, "") // no promote target
	store := newFakeStore()
	store.seedNamespace(tenantID, "github", "acme/svc", 8, "pr-svc-8")
	pub := &fakePublisher{}
	s := New(store, pub, kek)

	body := prBody("closed", 8, true, "svc") // merged=true
	out, _, err := s.HandleEvent(context.Background(), cfg, "github", body, signGitHub(testSecret, body), "pull_request")
	if err != nil || out != OutcomeTornDown {
		t.Fatalf("got (%v,%v), want (TORN_DOWN,nil)", out, err)
	}
	if len(store.promoteCalls) != 0 {
		t.Fatalf("no promote target => promote must not run, got %d calls", len(store.promoteCalls))
	}
	if store.tearDownCalls != 1 {
		t.Fatalf("teardown calls = %d, want 1", store.tearDownCalls)
	}
}

func TestHandleEvent_MergedWithTargetPromotesAndTearsDown(t *testing.T) {
	tenantID := uuid.New()
	cfg, kek := baseCfg(t, tenantID, "production")
	store := newFakeStore()
	ns := store.seedNamespace(tenantID, "github", "acme/svc", 11, "pr-svc-11")
	seedRepoTags(store, ns.OrgID.String(), map[string][]string{
		"api": {"latest", "v1"},
		"web": {"latest"},
	})
	pub := &fakePublisher{}
	s := New(store, pub, kek)

	body := prBody("closed", 11, true, "svc")
	out, org, err := s.HandleEvent(context.Background(), cfg, "github", body, signGitHub(testSecret, body), "pull_request")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != OutcomePromotedAndTornDown {
		t.Fatalf("out = %v, want PROMOTED_AND_TORN_DOWN", out)
	}
	if org != "pr-svc-11" {
		t.Fatalf("org = %q", org)
	}
	// 3 (repo,tag) pairs promoted.
	if len(store.promoteCalls) != 3 {
		t.Fatalf("promote calls = %d, want 3", len(store.promoteCalls))
	}
	for _, in := range store.promoteCalls {
		if in.DstOrg != "production" || in.SrcOrg != "pr-svc-11" {
			t.Fatalf("promote input orgs wrong: %+v", in)
		}
		if in.DstRepo != in.SrcRepo || in.DstTag != in.SrcTag {
			t.Fatalf("promote should keep repo+tag names: %+v", in)
		}
		if !in.CreateIfMissing {
			t.Fatalf("promote must set CreateIfMissing")
		}
		if in.Note != "PR #11 merge" {
			t.Fatalf("promote note = %q", in.Note)
		}
	}
	// Teardown ran AFTER promote.
	if store.tearDownCalls != 1 {
		t.Fatalf("teardown calls = %d, want 1", store.tearDownCalls)
	}
	// Torn-down event carries Promoted=true + target.
	var td events.PRNamespaceTornDownPayload
	found := false
	for _, e := range pub.published {
		if e.routingKey == events.RoutingPRNamespaceTornDown {
			_ = json.Unmarshal(e.evt.Payload, &td)
			found = true
		}
	}
	if !found || !td.Promoted || td.TargetOrg != "production" {
		t.Fatalf("torn_down payload = %+v (found=%v)", td, found)
	}
}

func TestHandleEvent_MergedImmutableDestSkipped(t *testing.T) {
	tenantID := uuid.New()
	cfg, kek := baseCfg(t, tenantID, "production")
	store := newFakeStore()
	ns := store.seedNamespace(tenantID, "github", "acme/svc", 12, "pr-svc-12")
	seedRepoTags(store, ns.OrgID.String(), map[string][]string{
		"api": {"latest", "v1"},
	})
	// "v1" is immutable at the destination — must be skipped, not fatal.
	store.promoteErr["api:v1"] = repository.ErrImmutableTag
	pub := &fakePublisher{}
	s := New(store, pub, kek)

	body := prBody("closed", 12, true, "svc")
	out, _, err := s.HandleEvent(context.Background(), cfg, "github", body, signGitHub(testSecret, body), "pull_request")
	if err != nil {
		t.Fatalf("immutable dest must be skipped, not fail: %v", err)
	}
	if out != OutcomePromotedAndTornDown {
		t.Fatalf("out = %v, want PROMOTED_AND_TORN_DOWN", out)
	}
	// Both tags attempted; teardown still ran.
	if len(store.promoteCalls) != 2 {
		t.Fatalf("promote calls = %d, want 2 (both attempted)", len(store.promoteCalls))
	}
	if store.tearDownCalls != 1 {
		t.Fatalf("teardown must still run after an immutable skip")
	}
}

func TestHandleEvent_MergedPromoteErrorAbortsBeforeTeardown(t *testing.T) {
	tenantID := uuid.New()
	cfg, kek := baseCfg(t, tenantID, "production")
	store := newFakeStore()
	ns := store.seedNamespace(tenantID, "github", "acme/svc", 13, "pr-svc-13")
	seedRepoTags(store, ns.OrgID.String(), map[string][]string{
		"api": {"latest"},
	})
	boom := status.Error(codes.Internal, "storage exploded")
	store.promoteErr["api:latest"] = boom
	pub := &fakePublisher{}
	s := New(store, pub, kek)

	body := prBody("closed", 13, true, "svc")
	out, _, err := s.HandleEvent(context.Background(), cfg, "github", body, signGitHub(testSecret, body), "pull_request")
	if err == nil {
		t.Fatalf("expected the promote error to surface")
	}
	if out != OutcomeIgnored {
		t.Fatalf("out = %v, want IGNORED on abort", out)
	}
	// The namespace must survive: teardown NOT called, no torn_down event.
	if store.tearDownCalls != 0 {
		t.Fatalf("teardown must NOT run when promote aborts (namespace survives for retry)")
	}
	for _, e := range pub.published {
		if e.routingKey == events.RoutingPRNamespaceTornDown {
			t.Fatalf("no torn_down event should publish on promote abort")
		}
	}
}

// TestHandleEvent_MalformedBodyAfterVerifyIgnored ensures a signed-but-broken
// body degrades to Ignored, never a panic/500.
func TestHandleEvent_MalformedBodyAfterVerifyIgnored(t *testing.T) {
	tenantID := uuid.New()
	cfg, kek := baseCfg(t, tenantID, "")
	body := []byte(`{"action":`) // valid signature over invalid json
	s := New(newFakeStore(), &fakePublisher{}, kek)
	out, _, err := s.HandleEvent(context.Background(), cfg, "github", body, signGitHub(testSecret, body), "pull_request")
	if err != nil || out != OutcomeIgnored {
		t.Fatalf("got (%v,%v), want (IGNORED,nil)", out, err)
	}
}

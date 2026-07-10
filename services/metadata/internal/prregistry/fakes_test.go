package prregistry

// fakes_test.go — in-memory Store + Publisher fakes shared across the
// prregistry unit tests. No DB, no broker. Each fake records the calls it
// received so a test can assert both the effect (state) and the exact
// (routingKey, payload) events emitted.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"

	"github.com/google/uuid"

	cryptoaes "github.com/steveokay/oci-janus/libs/crypto/aes"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
)

// fakeStore is a programmable in-memory Store. Fields prefixed with `err`
// force the corresponding method to fail; the call-count / capture fields let
// tests assert what happened.
type fakeStore struct {
	// namespaces keyed by (provider, sourceRepo, prNumber).
	namespaces map[string]*repository.PRNamespace

	// repos returned by ListRepositories for the given orgID.
	reposByOrg map[string][]*metadatav1.Repository
	// tags returned by ListTags for the given repoID (single page — tests
	// keep counts under tagPageSize).
	tagsByRepo map[string][]*metadatav1.Tag

	// promoteErr is consulted per (repo, tag) key "repo:tag" — a non-nil
	// value makes that PromoteTag call fail with it.
	promoteErr map[string]error

	// getOrCreateOrgErr / upsertNSErr / tearDownErr force those methods to
	// fail when set.
	getOrCreateOrgErr error
	upsertNSErr       error
	tearDownErr       error

	// getNamespaceErr, when set, is returned by GetPRNamespace (e.g.
	// repository.ErrNotFound).
	getNamespaceErr error

	// existingOrgs (name -> id) models orgs that already exist in the DB for
	// the SEC-085 adoption guard: LookupOrgIDByName returns a hit here, else
	// repository.ErrNotFound. lookupOrgErr forces the error path.
	existingOrgs map[string]string
	lookupOrgErr error

	// Call captures.
	promoteCalls  []repository.PromoteTagInput
	tearDownCalls int
	upsertCalls   []repository.PRNamespace
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		namespaces: map[string]*repository.PRNamespace{},
		reposByOrg: map[string][]*metadatav1.Repository{},
		tagsByRepo: map[string][]*metadatav1.Tag{},
		promoteErr: map[string]error{},
	}
}

func nsKey(provider, sourceRepo string, prNumber int) string {
	return provider + "|" + sourceRepo + "|" + uuid.NewSHA1(uuid.Nil, []byte{byte(prNumber)}).String()
}

func (f *fakeStore) GetOrCreateOrganization(_ context.Context, _ /*tenantID*/, orgName string) (string, error) {
	if f.getOrCreateOrgErr != nil {
		return "", f.getOrCreateOrgErr
	}
	// Deterministic org id derived from the name so repeat calls are stable.
	return uuid.NewSHA1(uuid.Nil, []byte(orgName)).String(), nil
}

func (f *fakeStore) LookupOrgIDByName(_ context.Context, _ /*tenantID*/, orgName string) (string, error) {
	if f.lookupOrgErr != nil {
		return "", f.lookupOrgErr
	}
	if id, ok := f.existingOrgs[orgName]; ok {
		return id, nil
	}
	return "", repository.ErrNotFound
}

func (f *fakeStore) UpsertPRNamespace(_ context.Context, ns repository.PRNamespace) (*repository.PRNamespace, error) {
	f.upsertCalls = append(f.upsertCalls, ns)
	if f.upsertNSErr != nil {
		return nil, f.upsertNSErr
	}
	out := ns
	if out.ID == uuid.Nil {
		out.ID = uuid.New()
	}
	out.Status = "active"
	f.namespaces[nsKey(ns.Provider, ns.SourceRepo, ns.PRNumber)] = &out
	return &out, nil
}

func (f *fakeStore) GetPRNamespace(_ context.Context, _ uuid.UUID, provider, sourceRepo string, prNumber int) (*repository.PRNamespace, error) {
	if f.getNamespaceErr != nil {
		return nil, f.getNamespaceErr
	}
	ns, ok := f.namespaces[nsKey(provider, sourceRepo, prNumber)]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return ns, nil
}

func (f *fakeStore) TearDownPRNamespace(_ context.Context, _ /*tenantID*/, namespaceID, _ uuid.UUID) error {
	f.tearDownCalls++
	if f.tearDownErr != nil {
		return f.tearDownErr
	}
	for _, ns := range f.namespaces {
		if ns.ID == namespaceID {
			ns.Status = "torn_down"
			ns.OrgID = nil
		}
	}
	return nil
}

func (f *fakeStore) ListRepositories(_ context.Context, _ /*tenantID*/, orgID, _ string) ([]*metadatav1.Repository, error) {
	return f.reposByOrg[orgID], nil
}

func (f *fakeStore) ListTags(_ context.Context, _ /*tenantID*/, repoID string, _ int32, last string) ([]*metadatav1.Tag, error) {
	// Single-page fake: return everything on the first call (last==""), and
	// an empty page thereafter so the promote loop terminates.
	if last != "" {
		return nil, nil
	}
	return f.tagsByRepo[repoID], nil
}

func (f *fakeStore) PromoteTag(_ context.Context, in repository.PromoteTagInput) (*metadatav1.Promotion, error) {
	f.promoteCalls = append(f.promoteCalls, in)
	if err := f.promoteErr[in.DstRepo+":"+in.DstTag]; err != nil {
		return nil, err
	}
	return &metadatav1.Promotion{}, nil
}

// seedNamespace inserts an active namespace row directly (bypassing upsert) so
// teardown/promote tests start from a provisioned state.
func (f *fakeStore) seedNamespace(tenantID uuid.UUID, provider, sourceRepo string, prNumber int, orgName string) *repository.PRNamespace {
	orgID := uuid.NewSHA1(uuid.Nil, []byte(orgName))
	ns := &repository.PRNamespace{
		ID:         uuid.New(),
		TenantID:   tenantID,
		OrgID:      &orgID,
		Provider:   provider,
		SourceRepo: sourceRepo,
		PRNumber:   prNumber,
		OrgName:    orgName,
		Status:     "active",
	}
	f.namespaces[nsKey(provider, sourceRepo, prNumber)] = ns
	return ns
}

// publishedEvent is one captured (routingKey, Event) pair.
type publishedEvent struct {
	routingKey string
	evt        events.Event
}

// fakePublisher records every Publish call. Publish never fails (the Service
// treats publish as best-effort anyway).
type fakePublisher struct {
	published []publishedEvent
	err       error
}

func (p *fakePublisher) Publish(_ context.Context, routingKey string, evt events.Event) error {
	p.published = append(p.published, publishedEvent{routingKey: routingKey, evt: evt})
	return p.err
}

// --- test crypto helpers ---------------------------------------------------

// testKEK is a fixed 32-byte AES-256 key for the verify tests.
func testKEK() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

// sealSecret encrypts a webhook secret with the KEK using the same
// libs/crypto/aes codec the production Verify path expects.
func sealSecret(t testingTB, secret string, kek []byte) []byte {
	t.Helper()
	ct, err := cryptoaes.Encrypt([]byte(secret), kek)
	if err != nil {
		t.Fatalf("seal secret: %v", err)
	}
	return ct
}

// signGitHub computes the X-Hub-Signature-256 header value for body signed
// with secret, matching GitHub's "sha256=<hex>" format.
func signGitHub(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// testingTB is the minimal subset of *testing.T the helpers need — declared so
// the helpers don't force a testing import into every caller file.
type testingTB interface {
	Helper()
	Fatalf(format string, args ...any)
}

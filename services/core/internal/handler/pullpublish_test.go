// FE-API-042 — handler-level verification that handleGetManifest publishes a
// pull.image event ONLY when the manifest is actually served (status 200).
// The 404 / 401 paths must short-circuit BEFORE the publish so a missing
// manifest never gets recorded as a pull.
package handler

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	storagev1 "github.com/steveokay/oci-janus/proto/gen/go/storage/v1"

	"context"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/core/internal/service"
)

// countingPublisher records each Publish call so the test can assert on the
// post-200 publish path without standing up a real broker. Mirrors the shape
// of the recordingPublisher used in service-package tests.
type countingPublisher struct {
	mu    sync.Mutex
	calls []events.Event
}

func (p *countingPublisher) Publish(_ context.Context, _ string, evt events.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, evt)
	return nil
}

func (p *countingPublisher) snapshot() []events.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]events.Event, len(p.calls))
	copy(out, p.calls)
	return out
}

// countByType returns the number of recorded events with the given routing key.
func (p *countingPublisher) countByType(t string) int {
	n := 0
	for _, e := range p.snapshot() {
		if e.Type == t {
			n++
		}
	}
	return n
}

// pullTestCtx is the per-test wiring for the FE-API-042 handler tests.
type pullTestCtx struct {
	srv  *httptest.Server
	meta *handlerFakeMetaServer
	pub  *countingPublisher
}

// buildPullPublishHarness mirrors buildHandlerServer but threads through a
// countingPublisher so the test can verify pull.image emission. We can't reuse
// buildHandlerServer because it pins noopEventPublisher.
func buildPullPublishHarness(t *testing.T) (*pullTestCtx, func()) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	fakeAuth := &handlerFakeAuthServer{}
	authLis := bufconn.Listen(bufSize)
	authSrv := grpc.NewServer()
	authv1.RegisterAuthServiceServer(authSrv, fakeAuth)
	go func() { _ = authSrv.Serve(authLis) }()

	fakeMeta := newHandlerFakeMetaServer()
	metaLis := bufconn.Listen(bufSize)
	metaSrv := grpc.NewServer()
	metadatav1.RegisterMetadataServiceServer(metaSrv, fakeMeta)
	go func() { _ = metaSrv.Serve(metaLis) }()

	fakeStorage := newHandlerFakeStorageServer()
	storageLis := bufconn.Listen(bufSize)
	storageSrv := grpc.NewServer()
	storagev1.RegisterStorageServiceServer(storageSrv, fakeStorage)
	go func() { _ = storageSrv.Serve(storageLis) }()

	dialBuf := func(lis *bufconn.Listener) *grpc.ClientConn {
		conn, err := grpc.NewClient(
			"passthrough://bufnet",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return lis.DialContext(ctx)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			t.Fatalf("grpc.NewClient: %v", err)
		}
		return conn
	}
	authConn := dialBuf(authLis)
	metaConn := dialBuf(metaLis)
	storageConn := dialBuf(storageLis)

	authClient := service.NewAuthClient(authConn, rdb)
	uploads := service.NewUploadStore(rdb)
	refs := service.NewReferrerStore(rdb)
	pub := &countingPublisher{}
	reg := service.NewRegistryWithClients(
		metadatav1.NewMetadataServiceClient(metaConn),
		storagev1.NewStorageServiceClient(storageConn),
		uploads,
		refs,
		pub,
	)

	h := New(authClient, reg, "https://auth.example.com/token")
	mux := http.NewServeMux()
	h.Register(mux)

	srv := httptest.NewServer(mux)
	ctx := &pullTestCtx{srv: srv, meta: fakeMeta, pub: pub}
	cleanup := func() {
		srv.Close()
		authSrv.Stop()
		metaSrv.Stop()
		storageSrv.Stop()
		_ = authConn.Close()
		_ = metaConn.Close()
		_ = storageConn.Close()
		_ = rdb.Close()
		mr.Close()
	}
	return ctx, cleanup
}

// pushManifest helper: PUTs a manifest at the given tag and returns the body.
func pushManifest(t *testing.T, srvURL, name, tag string) []byte {
	t.Helper()
	rawJSON := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:` + strings.Repeat("e", 64) + `","size":7},"layers":[]}`)
	putReq := bearerReqTest(t, http.MethodPut, srvURL+"/v2/"+name+"/manifests/"+tag, bytes.NewReader(rawJSON))
	putReq.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	resp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatalf("PUT manifest: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT manifest status: got %d, want 201", resp.StatusCode)
	}
	return rawJSON
}

// bearerReqTest is a local copy of bearerReq so this file doesn't depend on
// test-only ordering between files for compile.
func bearerReqTest(t *testing.T, method, url string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer valid-token")
	return req
}

// TestHandleGetManifest_success_publishesPullImage verifies that a successful
// 200 GET produces exactly one pull.image event. We subtract any push.completed
// events the PUT generated so the assertion isolates the new behaviour.
func TestHandleGetManifest_success_publishesPullImage(t *testing.T) {
	tc, cleanup := buildPullPublishHarness(t)
	defer cleanup()

	pushManifest(t, tc.srv.URL, "myorg/myrepo", "v1.0")
	pullEventsBefore := tc.pub.countByType(events.RoutingPullImage)

	getReq := bearerReqTest(t, http.MethodGet, tc.srv.URL+"/v2/myorg/myrepo/manifests/v1.0", nil)
	resp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("GET manifest: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status: got %d, want 200", resp.StatusCode)
	}

	pullEventsAfter := tc.pub.countByType(events.RoutingPullImage)
	if delta := pullEventsAfter - pullEventsBefore; delta != 1 {
		t.Errorf("pull.image events after 200 GET: got delta=%d, want 1", delta)
	}
}

// TestHandleGetManifest_notFound_doesNotPublish ensures a 404 short-circuits
// before the publish — if it didn't, FE-API-030 analytics would over-count
// pulls that never actually delivered a manifest body.
func TestHandleGetManifest_notFound_doesNotPublish(t *testing.T) {
	tc, cleanup := buildPullPublishHarness(t)
	defer cleanup()

	// Seed the repo so the 404 is on the manifest, not the repo.
	pushManifest(t, tc.srv.URL, "myorg/myrepo", "real-tag")
	pullEventsBefore := tc.pub.countByType(events.RoutingPullImage)

	getReq := bearerReqTest(t, http.MethodGet, tc.srv.URL+"/v2/myorg/myrepo/manifests/no-such-tag", nil)
	resp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("GET manifest: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET status: got %d, want 404", resp.StatusCode)
	}

	if pullEventsAfter := tc.pub.countByType(events.RoutingPullImage); pullEventsAfter != pullEventsBefore {
		t.Errorf("pull.image events after 404: got %d, want %d (publish must be skipped)", pullEventsAfter, pullEventsBefore)
	}
}

// TestHandleGetManifest_unauthorised_doesNotPublish ensures an auth failure
// short-circuits before the publish so an attacker probing repos can't seed
// fake pull-activity rows into the audit trail.
func TestHandleGetManifest_unauthorised_doesNotPublish(t *testing.T) {
	tc, cleanup := buildPullPublishHarness(t)
	defer cleanup()

	pushManifest(t, tc.srv.URL, "myorg/myrepo", "v1.0")
	pullEventsBefore := tc.pub.countByType(events.RoutingPullImage)

	// No-access-token: server validates the bearer but no repository access.
	getReq, _ := http.NewRequest(http.MethodGet, tc.srv.URL+"/v2/myorg/myrepo/manifests/v1.0", nil)
	getReq.Header.Set("Authorization", "Bearer no-access-token")
	resp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("GET manifest: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET status: got %d, want 401 or 403", resp.StatusCode)
	}
	if pullEventsAfter := tc.pub.countByType(events.RoutingPullImage); pullEventsAfter != pullEventsBefore {
		t.Errorf("pull.image events after auth fail: got %d, want %d (publish must be skipped)", pullEventsAfter, pullEventsBefore)
	}
}

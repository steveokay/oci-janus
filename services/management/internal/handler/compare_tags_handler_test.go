// compare_tags_handler_test.go — HTTP-level tests for GET …/compare.
//
// Uses a dedicated scriptable metadata fake (compareMetaServer) because the
// shared fakeMetaServer.GetTag returns a single fixed digest for every tag,
// which can't express "two tags at two different digests" — the whole premise
// of a diff. Reuses the shared fakeAuthServer (readerToken → reader on myorg)
// and fakeCoreServer (config blobs).
package handler_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	corev1 "github.com/steveokay/oci-janus/proto/gen/go/core/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

// compareMetaServer maps tag→digest and digest→(manifest, sbom, scan) so a
// case can script two distinct tags. Absent SBOM/scan entries return NotFound
// so the graceful-degradation paths are reachable.
type compareMetaServer struct {
	metadatav1.UnimplementedMetadataServiceServer

	tags      map[string]string // tag name -> digest
	manifests map[string][]byte // digest -> raw_json
	sizes     map[string]int64  // digest -> size_bytes
	mediaType map[string]string // digest -> manifest media type (default image manifest)
	sboms     map[string][]byte // digest -> sbom bytes (absent -> NotFound)
	scans     map[string][]byte // digest -> findings_json (absent -> NotFound)
}

func (s *compareMetaServer) GetRepositoryByName(_ context.Context, req *metadatav1.GetRepositoryByNameRequest) (*metadatav1.Repository, error) {
	if req.GetName() == "myorg/myrepo" {
		return &metadatav1.Repository{RepoId: testRepoID, OrgId: testOrgID, Name: "myorg/myrepo", CreatedAt: timestamppb.Now()}, nil
	}
	return nil, status.Error(codes.NotFound, "repo not found")
}

func (s *compareMetaServer) GetTag(_ context.Context, req *metadatav1.GetTagRequest) (*metadatav1.Tag, error) {
	digest, ok := s.tags[req.GetName()]
	if !ok {
		return nil, status.Error(codes.NotFound, "tag not found")
	}
	return &metadatav1.Tag{Name: req.GetName(), ManifestDigest: digest, SizeBytes: s.sizes[digest]}, nil
}

func (s *compareMetaServer) GetManifest(_ context.Context, req *metadatav1.GetManifestRequest) (*metadatav1.Manifest, error) {
	raw, ok := s.manifests[req.GetReference()]
	if !ok {
		return nil, status.Error(codes.NotFound, "manifest not found")
	}
	mt := s.mediaType[req.GetReference()]
	if mt == "" {
		mt = "application/vnd.oci.image.manifest.v1+json"
	}
	return &metadatav1.Manifest{Digest: req.GetReference(), RawJson: raw, SizeBytes: s.sizes[req.GetReference()], MediaType: mt}, nil
}

func (s *compareMetaServer) GetScanSBOM(_ context.Context, req *metadatav1.GetScanSBOMRequest) (*metadatav1.GetScanSBOMResponse, error) {
	sbom, ok := s.sboms[req.GetManifestDigest()]
	if !ok {
		return nil, status.Error(codes.NotFound, "no sbom")
	}
	return &metadatav1.GetScanSBOMResponse{Format: "spdx-json", SbomJson: sbom}, nil
}

func (s *compareMetaServer) GetScanResult(_ context.Context, req *metadatav1.GetScanResultRequest) (*metadatav1.ScanResult, error) {
	findings, ok := s.scans[req.GetManifestDigest()]
	if !ok {
		return nil, status.Error(codes.NotFound, "no scan")
	}
	return &metadatav1.ScanResult{Status: "complete", FindingsJson: findings}, nil
}

// compareTestEnv wraps the httptest server + the scriptable fakes.
type compareTestEnv struct {
	srv  *httptest.Server
	meta *compareMetaServer
	core *fakeCoreServer
}

// newCompareTestEnv wires auth/audit + the scriptable compare meta, and
// optionally a fake core (withCore=false leaves h.core nil so the config
// section degrades to unavailable).
func newCompareTestEnv(t *testing.T, meta *compareMetaServer, withCore bool) *compareTestEnv {
	t.Helper()

	authLis := bufconn.Listen(bufSize)
	authGRPC := grpc.NewServer()
	authv1.RegisterAuthServiceServer(authGRPC, &fakeAuthServer{})
	healthpb.RegisterHealthServer(authGRPC, &fakeHealthServer{})
	go func() { _ = authGRPC.Serve(authLis) }()
	t.Cleanup(authGRPC.Stop)

	metaLis := bufconn.Listen(bufSize)
	metaGRPC := grpc.NewServer()
	metadatav1.RegisterMetadataServiceServer(metaGRPC, meta)
	healthpb.RegisterHealthServer(metaGRPC, &fakeHealthServer{})
	go func() { _ = metaGRPC.Serve(metaLis) }()
	t.Cleanup(metaGRPC.Stop)

	auditLis := bufconn.Listen(bufSize)
	auditGRPC := grpc.NewServer()
	auditv1.RegisterAuditServiceServer(auditGRPC, &fakeAuditServer{})
	healthpb.RegisterHealthServer(auditGRPC, &fakeHealthServer{})
	go func() { _ = auditGRPC.Serve(auditLis) }()
	t.Cleanup(auditGRPC.Stop)

	dial := func(lis *bufconn.Listener) *grpc.ClientConn {
		conn, err := grpc.NewClient("passthrough:///bufnet",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			t.Fatalf("dial bufconn: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}

	h := handler.New(
		authv1.NewAuthServiceClient(dial(authLis)),
		metadatav1.NewMetadataServiceClient(dial(metaLis)),
		auditv1.NewAuditServiceClient(dial(auditLis)),
		nil,
		"",
	)

	env := &compareTestEnv{meta: meta}
	if withCore {
		env.core = &fakeCoreServer{}
		coreLis := bufconn.Listen(bufSize)
		coreGRPC := grpc.NewServer()
		corev1.RegisterCoreServiceServer(coreGRPC, env.core)
		go func() { _ = coreGRPC.Serve(coreLis) }()
		t.Cleanup(coreGRPC.Stop)
		h = h.WithCoreClient(corev1.NewCoreServiceClient(dial(coreLis)))
	}

	mux := http.NewServeMux()
	h.Register(mux)
	env.srv = httptest.NewServer(mux)
	t.Cleanup(env.srv.Close)
	return env
}

func (e *compareTestEnv) get(t *testing.T, path, token string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// compareWire mirrors the compareResponse JSON for assertions.
type compareWire struct {
	From struct {
		Tag, Digest string
		SizeBytes   int64 `json:"size_bytes"`
	} `json:"from"`
	To struct {
		Tag string
	} `json:"to"`
	Layers struct {
		Added          []struct{ Digest string } `json:"added"`
		Removed        []struct{ Digest string } `json:"removed"`
		CommonCount    int                       `json:"common_count"`
		SizeDeltaBytes int64                     `json:"size_delta_bytes"`
	} `json:"layers"`
	Config struct {
		Available bool `json:"available"`
		Env       struct {
			Changed []struct{ Key, From, To string } `json:"changed"`
		} `json:"env"`
	} `json:"config"`
	Packages struct {
		Available bool                             `json:"available"`
		Added     []struct{ Name, Version string } `json:"added"`
		Removed   []struct{ Name string }          `json:"removed"`
		Changed   []struct {
			Name        string `json:"name"`
			FromVersion string `json:"from_version"`
			ToVersion   string `json:"to_version"`
		} `json:"changed"`
	} `json:"packages"`
	Vulnerabilities struct {
		Available bool `json:"available"`
		Added     []struct {
			CVE string `json:"cve"`
		} `json:"added"`
		Removed []struct {
			CVE string `json:"cve"`
		} `json:"removed"`
	} `json:"vulnerabilities"`
}

// twoTagMeta seeds a meta with two tags at two digests: layers [a,b]→[b,c],
// size 300→550, config cfg1/cfg2, SBOMs + scans set per the happy-path story.
func twoTagMeta() *compareMetaServer {
	man := func(cfg string, layers ...string) []byte {
		type desc struct {
			Digest    string `json:"digest"`
			Size      int64  `json:"size"`
			MediaType string `json:"mediaType"`
		}
		m := struct {
			Config desc   `json:"config"`
			Layers []desc `json:"layers"`
		}{Config: desc{Digest: cfg, Size: 10, MediaType: "cfg"}}
		for _, l := range layers {
			m.Layers = append(m.Layers, desc{Digest: l, Size: 100, MediaType: "layer"})
		}
		b, _ := json.Marshal(m)
		return b
	}
	return &compareMetaServer{
		tags:  map[string]string{"v1": "sha256:d1", "v2": "sha256:d2"},
		sizes: map[string]int64{"sha256:d1": 300, "sha256:d2": 550},
		manifests: map[string][]byte{
			"sha256:d1": man("sha256:cfg1", "sha256:a", "sha256:b"),
			"sha256:d2": man("sha256:cfg2", "sha256:b", "sha256:c"),
		},
		sboms: map[string][]byte{
			"sha256:d1": []byte(`{"packages":[{"name":"openssl","versionInfo":"3.0.1"},{"name":"wget","versionInfo":"1.21"}]}`),
			"sha256:d2": []byte(`{"packages":[{"name":"openssl","versionInfo":"3.0.2"},{"name":"curl","versionInfo":"8.5"}]}`),
		},
		scans: map[string][]byte{
			"sha256:d1": []byte(`[{"CVE":"CVE-2024-A","Severity":"HIGH","Package":"openssl"}]`),
			"sha256:d2": []byte(`[{"CVE":"CVE-2024-C","Severity":"MEDIUM","Package":"curl"}]`),
		},
	}
}

// TestCompareTags_HappyPath_AllSections drives the full diff with core wired
// and both tags scanned: every section must be available with correct deltas.
func TestCompareTags_HappyPath_AllSections(t *testing.T) {
	env := newCompareTestEnv(t, twoTagMeta(), true)
	env.core.blobs = map[string][]byte{
		"sha256:cfg1": []byte(`{"config":{"Env":["NODE_ENV=prod"],"Cmd":["nginx"]}}`),
		"sha256:cfg2": []byte(`{"config":{"Env":["NODE_ENV=stage"],"Cmd":["nginx"]}}`),
	}

	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/compare?from=v1&to=v2", readerToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var got compareWire
	decodeJSON(t, resp, &got)

	// Layers: +c, -a, common b, size +250.
	if len(got.Layers.Added) != 1 || got.Layers.Added[0].Digest != "sha256:c" {
		t.Errorf("layers added: want [c], got %+v", got.Layers.Added)
	}
	if len(got.Layers.Removed) != 1 || got.Layers.Removed[0].Digest != "sha256:a" {
		t.Errorf("layers removed: want [a], got %+v", got.Layers.Removed)
	}
	if got.Layers.CommonCount != 1 || got.Layers.SizeDeltaBytes != 250 {
		t.Errorf("layers common/delta: want 1/+250, got %d/%d", got.Layers.CommonCount, got.Layers.SizeDeltaBytes)
	}
	// Config: NODE_ENV changed prod→stage.
	if !got.Config.Available {
		t.Fatal("config should be available with core wired + both configs present")
	}
	if len(got.Config.Env.Changed) != 1 || got.Config.Env.Changed[0].Key != "NODE_ENV" ||
		got.Config.Env.Changed[0].From != "prod" || got.Config.Env.Changed[0].To != "stage" {
		t.Errorf("config env changed: want NODE_ENV prod→stage, got %+v", got.Config.Env.Changed)
	}
	// Packages: +curl -wget ~openssl.
	if !got.Packages.Available {
		t.Fatal("packages should be available")
	}
	if len(got.Packages.Added) != 1 || got.Packages.Added[0].Name != "curl" {
		t.Errorf("packages added: want curl, got %+v", got.Packages.Added)
	}
	if len(got.Packages.Removed) != 1 || got.Packages.Removed[0].Name != "wget" {
		t.Errorf("packages removed: want wget, got %+v", got.Packages.Removed)
	}
	if len(got.Packages.Changed) != 1 || got.Packages.Changed[0].Name != "openssl" {
		t.Errorf("packages changed: want openssl, got %+v", got.Packages.Changed)
	}
	// Vulns: +CVE-C -CVE-A.
	if !got.Vulnerabilities.Available {
		t.Fatal("vulns should be available")
	}
	if len(got.Vulnerabilities.Added) != 1 || got.Vulnerabilities.Added[0].CVE != "CVE-2024-C" {
		t.Errorf("vulns added: want CVE-2024-C, got %+v", got.Vulnerabilities.Added)
	}
	if len(got.Vulnerabilities.Removed) != 1 || got.Vulnerabilities.Removed[0].CVE != "CVE-2024-A" {
		t.Errorf("vulns removed: want CVE-2024-A, got %+v", got.Vulnerabilities.Removed)
	}
}

// TestCompareTags_DegradesWhenDataMissing — no core wired, and the target tag
// has neither SBOM nor scan. Layers still diff; config/packages/vulns each
// report available=false without failing the request.
func TestCompareTags_DegradesWhenDataMissing(t *testing.T) {
	meta := twoTagMeta()
	delete(meta.sboms, "sha256:d2")          // target has no SBOM
	delete(meta.scans, "sha256:d2")          // target has no scan
	env := newCompareTestEnv(t, meta, false) // no core

	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/compare?from=v1&to=v2", readerToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var got compareWire
	decodeJSON(t, resp, &got)

	if got.Layers.SizeDeltaBytes != 250 {
		t.Errorf("layers should still diff, got delta %d", got.Layers.SizeDeltaBytes)
	}
	if got.Config.Available {
		t.Error("config should be unavailable with no core wired")
	}
	if got.Packages.Available {
		t.Error("packages should be unavailable when the target has no SBOM")
	}
	if got.Vulnerabilities.Available {
		t.Error("vulns should be unavailable when the target is unscanned")
	}
}

// TestCompareTags_UnknownTag_returns404 — a `to` tag that doesn't exist.
func TestCompareTags_UnknownTag_returns404(t *testing.T) {
	env := newCompareTestEnv(t, twoTagMeta(), true)
	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/compare?from=v1&to=nope", readerToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

// TestCompareTags_InvalidTagParam_returns400 — a malformed `from` tag.
func TestCompareTags_InvalidTagParam_returns400(t *testing.T) {
	env := newCompareTestEnv(t, twoTagMeta(), true)
	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/compare?from=has%20space&to=v2", readerToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

package handler

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/steveokay/oci-janus/proto/gen/go/core/v1"
	"github.com/steveokay/oci-janus/services/core/internal/service"
)

// fakeReferrerLister is a hand-written stand-in for *service.Registry that lets
// the CoreHandler be tested without a live Redis referrer store.
type fakeReferrerLister struct {
	descs    []service.ReferrerDescriptor
	filtered bool
	err      error

	// captured args from the last call, for assertion.
	gotTenant, gotRepo, gotDigest, gotArtifactType string
}

func (f *fakeReferrerLister) GetReferrers(_ context.Context, tenantID, repoName, subjectDigest, artifactType string) ([]service.ReferrerDescriptor, bool, error) {
	f.gotTenant, f.gotRepo, f.gotDigest, f.gotArtifactType = tenantID, repoName, subjectDigest, artifactType
	if f.err != nil {
		return nil, false, f.err
	}
	return f.descs, f.filtered, nil
}

const validDigest = "sha256:" + "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

// TestListReferrers_validation exercises the request-validation branches, each
// of which must surface InvalidArgument before the registry is consulted.
func TestListReferrers_validation(t *testing.T) {
	cases := []struct {
		name string
		req  *corev1.ListReferrersRequest
	}{
		{"missing tenant", &corev1.ListReferrersRequest{Repository: "acme/app", SubjectDigest: validDigest}},
		{"missing repository", &corev1.ListReferrersRequest{TenantId: "t1", SubjectDigest: validDigest}},
		{"missing digest", &corev1.ListReferrersRequest{TenantId: "t1", Repository: "acme/app"}},
		{"malformed digest", &corev1.ListReferrersRequest{TenantId: "t1", Repository: "acme/app", SubjectDigest: "sha256:nothex"}},
		{"uppercase digest", &corev1.ListReferrersRequest{TenantId: "t1", Repository: "acme/app", SubjectDigest: "sha256:ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewCoreHandler(&fakeReferrerLister{})
			_, err := h.ListReferrers(context.Background(), tc.req)
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected InvalidArgument, got %v", err)
			}
		})
	}
}

// TestListReferrers_success verifies the happy path: descriptors are mapped
// field-for-field, the filtered flag is propagated, and the request fields are
// forwarded verbatim to the registry.
func TestListReferrers_success(t *testing.T) {
	fake := &fakeReferrerLister{
		descs: []service.ReferrerDescriptor{
			{
				MediaType:    "application/vnd.oci.image.manifest.v1+json",
				Digest:       validDigest,
				Size:         1234,
				ArtifactType: "application/vnd.example.sbom",
				Annotations:  map[string]string{"org.opencontainers.image.created": "2026-07-05"},
			},
		},
		filtered: true,
	}
	h := NewCoreHandler(fake)

	resp, err := h.ListReferrers(context.Background(), &corev1.ListReferrersRequest{
		TenantId:      "t1",
		Repository:    "acme/app",
		SubjectDigest: validDigest,
		ArtifactType:  "application/vnd.example.sbom",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Filtered {
		t.Errorf("expected filtered=true")
	}
	if len(resp.Referrers) != 1 {
		t.Fatalf("expected 1 referrer, got %d", len(resp.Referrers))
	}
	got := resp.Referrers[0]
	if got.MediaType != "application/vnd.oci.image.manifest.v1+json" || got.Digest != validDigest ||
		got.Size != 1234 || got.ArtifactType != "application/vnd.example.sbom" ||
		got.Annotations["org.opencontainers.image.created"] != "2026-07-05" {
		t.Errorf("descriptor not mapped correctly: %+v", got)
	}
	// Request fields forwarded verbatim.
	if fake.gotTenant != "t1" || fake.gotRepo != "acme/app" || fake.gotDigest != validDigest ||
		fake.gotArtifactType != "application/vnd.example.sbom" {
		t.Errorf("registry received wrong args: %+v", fake)
	}
}

// TestListReferrers_emptyResult confirms a repo with no referrers returns an
// empty (non-nil) slice and filtered=false, not an error.
func TestListReferrers_emptyResult(t *testing.T) {
	h := NewCoreHandler(&fakeReferrerLister{descs: nil, filtered: false})
	resp, err := h.ListReferrers(context.Background(), &corev1.ListReferrersRequest{
		TenantId:      "t1",
		Repository:    "acme/app",
		SubjectDigest: validDigest,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Filtered {
		t.Errorf("expected filtered=false")
	}
	if len(resp.Referrers) != 0 {
		t.Errorf("expected 0 referrers, got %d", len(resp.Referrers))
	}
}

// TestListReferrers_storeError maps an underlying store failure to Internal.
func TestListReferrers_storeError(t *testing.T) {
	h := NewCoreHandler(&fakeReferrerLister{err: errors.New("redis down")})
	_, err := h.ListReferrers(context.Background(), &corev1.ListReferrersRequest{
		TenantId:      "t1",
		Repository:    "acme/app",
		SubjectDigest: validDigest,
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal, got %v", err)
	}
}

package reportworker

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/scanner/internal/report"
)

// fakeMetaClient is a hand-written MetadataClient double. It returns a canned
// SecurityOverview and a scripted sequence of vulnerability pages, and records
// the page tokens the worker asked for so tests can assert pagination.
type fakeMetaClient struct {
	overview    *metadatav1.SecurityOverview
	overviewErr error

	// pages is returned in order, one per ListTenantVulnerabilities call.
	pages   []*metadatav1.ListTenantVulnerabilitiesResponse
	listErr error

	// recorded call inputs.
	overviewTenant string
	listTokens     []string
}

func (f *fakeMetaClient) GetSecurityOverview(_ context.Context, in *metadatav1.GetSecurityOverviewRequest, _ ...grpc.CallOption) (*metadatav1.SecurityOverview, error) {
	f.overviewTenant = in.GetTenantId()
	if f.overviewErr != nil {
		return nil, f.overviewErr
	}
	return f.overview, nil
}

func (f *fakeMetaClient) ListTenantVulnerabilities(_ context.Context, in *metadatav1.ListTenantVulnerabilitiesRequest, _ ...grpc.CallOption) (*metadatav1.ListTenantVulnerabilitiesResponse, error) {
	f.listTokens = append(f.listTokens, in.GetPageToken())
	if f.listErr != nil {
		return nil, f.listErr
	}
	idx := len(f.listTokens) - 1
	if idx >= len(f.pages) {
		return &metadatav1.ListTenantVulnerabilitiesResponse{}, nil
	}
	return f.pages[idx], nil
}

func newWorker(meta MetadataClient, cfg Config) *Worker {
	// repo is nil: buildDocument never touches it.
	return New(nil, meta, cfg)
}

func TestBuildDocument_PopulatesSummaryAndFindings(t *testing.T) {
	tenant := uuid.New()
	meta := &fakeMetaClient{
		overview: &metadatav1.SecurityOverview{
			SeverityCounts: &metadatav1.SecurityCounts{
				Critical: 2, High: 3, Medium: 1, Low: 4, Negligible: 5,
			},
		},
		// Two pages: the first hands back a next_page_token, the second is the tail.
		pages: []*metadatav1.ListTenantVulnerabilitiesResponse{
			{
				Vulnerabilities: []*metadatav1.TenantVulnerability{
					{CveId: "CVE-2024-0001", Severity: "CRITICAL", PackageName: "openssl", PackageVersion: "1.1.1", FixedIn: "1.1.1a"},
				},
				NextPageToken: "page2",
			},
			{
				Vulnerabilities: []*metadatav1.TenantVulnerability{
					{CveId: "CVE-2024-0002", Severity: "HIGH", PackageName: "zlib", PackageVersion: "1.2.11", FixedIn: "1.2.12"},
				},
			},
		},
	}

	w := newWorker(meta, Config{})
	doc, err := w.buildDocument(context.Background(), tenant.String())
	if err != nil {
		t.Fatalf("buildDocument: %v", err)
	}

	// Summary counts come from GetSecurityOverview — the authoritative totals.
	want := map[string]int{"CRITICAL": 2, "HIGH": 3, "MEDIUM": 1, "LOW": 4, "NEGLIGIBLE": 5}
	for sev, n := range want {
		if doc.SummaryCount[sev] != n {
			t.Errorf("SummaryCount[%s] = %d, want %d", sev, doc.SummaryCount[sev], n)
		}
	}

	// Findings are the flattened, paginated vulnerability list.
	if len(doc.Findings) != 2 {
		t.Fatalf("len(Findings) = %d, want 2", len(doc.Findings))
	}
	if doc.Findings[0].CVEID != "CVE-2024-0001" || doc.Findings[0].PackageName != "openssl" ||
		doc.Findings[0].Version != "1.1.1" || doc.Findings[0].FixedIn != "1.1.1a" || doc.Findings[0].Severity != "CRITICAL" {
		t.Errorf("Findings[0] mismapped: %+v", doc.Findings[0])
	}
	if doc.Findings[1].CVEID != "CVE-2024-0002" || doc.Findings[1].Version != "1.2.11" {
		t.Errorf("Findings[1] mismapped: %+v", doc.Findings[1])
	}

	// The report is tenant-scoped — the RPC must carry the report's tenant id.
	if meta.overviewTenant != tenant.String() {
		t.Errorf("GetSecurityOverview tenant = %q, want %q", meta.overviewTenant, tenant.String())
	}
	// Pagination: first call with an empty token, second with the returned token.
	if len(meta.listTokens) != 2 || meta.listTokens[0] != "" || meta.listTokens[1] != "page2" {
		t.Errorf("list page tokens = %v, want [\"\" \"page2\"]", meta.listTokens)
	}
	if doc.TenantID != tenant.String() {
		t.Errorf("doc.TenantID = %q, want %q", doc.TenantID, tenant.String())
	}
}

func TestBuildDocument_FindingsReachRenderedSBOM(t *testing.T) {
	// End-to-end within the scanner: real findings fetched from metadata must
	// appear in the rendered SPDX output — the whole point of FUT-080.
	meta := &fakeMetaClient{
		overview: &metadatav1.SecurityOverview{SeverityCounts: &metadatav1.SecurityCounts{Critical: 1}},
		pages: []*metadatav1.ListTenantVulnerabilitiesResponse{
			{Vulnerabilities: []*metadatav1.TenantVulnerability{
				{CveId: "CVE-2024-9999", Severity: "CRITICAL", PackageName: "libfoo", PackageVersion: "2.0"},
			}},
		},
	}
	w := newWorker(meta, Config{})

	doc, err := w.buildDocument(context.Background(), uuid.New().String())
	if err != nil {
		t.Fatalf("buildDocument: %v", err)
	}
	sbom, err := report.RenderSBOM(doc)
	if err != nil {
		t.Fatalf("RenderSBOM: %v", err)
	}
	if !bytes.Contains(sbom, []byte("CVE-2024-9999")) {
		t.Errorf("rendered SBOM does not contain the fetched CVE:\n%s", sbom)
	}
	if !bytes.Contains(sbom, []byte("libfoo")) {
		t.Errorf("rendered SBOM does not contain the fetched package name")
	}
}

func TestBuildDocument_OverviewError_ReturnsError(t *testing.T) {
	// FUT-080 invariant: a silently-wrong report is worse than none. If the
	// metadata service can't be reached, buildDocument must surface the error
	// so the worker marks the report failed rather than emitting all-zeros.
	meta := &fakeMetaClient{overviewErr: errors.New("metadata unavailable")}
	w := newWorker(meta, Config{})

	if _, err := w.buildDocument(context.Background(), uuid.New().String()); err == nil {
		t.Fatal("expected error when GetSecurityOverview fails, got nil")
	}
}

func TestBuildDocument_ListError_ReturnsError(t *testing.T) {
	meta := &fakeMetaClient{
		overview: &metadatav1.SecurityOverview{SeverityCounts: &metadatav1.SecurityCounts{High: 1}},
		listErr:  errors.New("metadata unavailable"),
	}
	w := newWorker(meta, Config{})

	if _, err := w.buildDocument(context.Background(), uuid.New().String()); err == nil {
		t.Fatal("expected error when ListTenantVulnerabilities fails, got nil")
	}
}

func TestBuildDocument_CapsFindings(t *testing.T) {
	// A tenant with a huge CVE backlog must not produce an unbounded report /
	// exhaust memory. The detailed findings list is capped; the summary counts
	// stay authoritative regardless.
	var vulns []*metadatav1.TenantVulnerability
	for range 10 {
		vulns = append(vulns, &metadatav1.TenantVulnerability{CveId: "CVE", Severity: "LOW"})
	}
	meta := &fakeMetaClient{
		overview: &metadatav1.SecurityOverview{SeverityCounts: &metadatav1.SecurityCounts{Low: 10}},
		pages: []*metadatav1.ListTenantVulnerabilitiesResponse{
			// One page that keeps offering more, but the cap should stop it.
			{Vulnerabilities: vulns, NextPageToken: "more"},
			{Vulnerabilities: vulns, NextPageToken: "more"},
			{Vulnerabilities: vulns, NextPageToken: "more"},
		},
	}
	w := newWorker(meta, Config{MaxFindings: 15})

	doc, err := w.buildDocument(context.Background(), uuid.New().String())
	if err != nil {
		t.Fatalf("buildDocument: %v", err)
	}
	if len(doc.Findings) > 15 {
		t.Errorf("len(Findings) = %d, want <= 15 (capped)", len(doc.Findings))
	}
	if doc.SummaryCount["LOW"] != 10 {
		t.Errorf("SummaryCount LOW = %d, want 10 (authoritative, uncapped)", doc.SummaryCount["LOW"])
	}
}

// Package reportworker drives the compliance-report background job.
//
// On a fixed interval the worker calls ClaimPendingReport (which executes a
// SELECT ... FOR UPDATE SKIP LOCKED so multiple scanner replicas coexist),
// renders the PDF + SPDX outputs to disk, and marks the row succeeded
// (or failed if anything along the way errors).
//
// All on-disk paths are constructed under cfg.OutputDir; the worker calls
// filepath.Clean and verifies the result stays under that root before
// writing — defence against a future row whose tenant_id or report_id was
// mutated outside SQL.
package reportworker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/scanner/internal/report"
	"github.com/steveokay/oci-janus/services/scanner/internal/repository"
)

const (
	// defaultMaxFindings caps how many detailed CVE rows a single report
	// embeds. The severity summary stays authoritative (it comes from the
	// aggregate GetSecurityOverview RPC); this bound only stops a tenant with
	// a huge backlog from producing an unbounded PDF / exhausting memory.
	defaultMaxFindings = 5000
	// findingsPageSize is the page_size sent to ListTenantVulnerabilities.
	findingsPageSize = 500
	// defaultRPCTimeout bounds each metadata call. The scanner dials metadata
	// without the shared client interceptor chain (no auto-deadline), so the
	// worker must set its own — CLAUDE.md §6.
	defaultRPCTimeout = 30 * time.Second
)

// MetadataClient is the narrow slice of registry-metadata's gRPC surface the
// report worker depends on. *metadatav1.MetadataServiceClient satisfies it;
// tests supply a double.
type MetadataClient interface {
	GetSecurityOverview(ctx context.Context, in *metadatav1.GetSecurityOverviewRequest, opts ...grpc.CallOption) (*metadatav1.SecurityOverview, error)
	ListTenantVulnerabilities(ctx context.Context, in *metadatav1.ListTenantVulnerabilitiesRequest, opts ...grpc.CallOption) (*metadatav1.ListTenantVulnerabilitiesResponse, error)
}

// Config tunes the worker.
type Config struct {
	OutputDir    string
	PollInterval time.Duration
	// MaxFindings caps the detailed CVE list embedded per report; 0 → default.
	MaxFindings int
	// RPCTimeout bounds each metadata gRPC call; 0 → default.
	RPCTimeout time.Duration
}

// Worker runs the report-generation loop. It is safe to run as many copies
// of Worker as there are scanner replicas — the DB poller arbitrates via
// FOR UPDATE SKIP LOCKED.
type Worker struct {
	repo *repository.Repository
	meta MetadataClient
	cfg  Config
}

// New constructs a Worker. meta is the registry-metadata client the worker
// queries for real severity counts + findings; the caller is responsible for
// ensuring OutputDir is creatable (the worker will MkdirAll under it).
func New(repo *repository.Repository, meta MetadataClient, cfg Config) *Worker {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if cfg.MaxFindings <= 0 {
		cfg.MaxFindings = defaultMaxFindings
	}
	if cfg.RPCTimeout <= 0 {
		cfg.RPCTimeout = defaultRPCTimeout
	}
	return &Worker{repo: repo, meta: meta, cfg: cfg}
}

// Run blocks until ctx is cancelled. Each tick the worker tries to claim
// one pending report and, if successful, processes it inline. Errors are
// logged but do not stop the loop — the next pending row gets a fresh
// attempt.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()
	// Do an immediate first attempt so tests don't have to wait a full tick.
	w.processOne(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.processOne(ctx)
		}
	}
}

// processOne attempts to claim and render exactly one pending report.
// Errors are logged + the row's status updated; no return value because
// neither caller (Run loop tick + tick-after-poke) acts on success vs
// no-row-available — both just loop and try again.
func (w *Worker) processOne(ctx context.Context) {
	rec, err := w.repo.ClaimPendingReport(ctx)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return
		}
		slog.Warn("ClaimPendingReport failed", "err", err)
		return
	}

	if err := w.renderAndPersist(ctx, rec); err != nil {
		// Best effort — if the FailReport call itself fails, log and move on.
		// The row stays "running" until a future operator intervenes.
		slog.Error("compliance report failed", "report_id", rec.ReportID, "err", err)
		if failErr := w.repo.FailReport(ctx, rec.ReportID, err.Error()); failErr != nil {
			slog.Error("FailReport", "report_id", rec.ReportID, "err", failErr)
		}
	}
}

// renderAndPersist writes PDF + SBOM bytes under OutputDir and marks the
// report succeeded.
func (w *Worker) renderAndPersist(ctx context.Context, rec *repository.ComplianceReport) error {
	// Fetch the real severity summary + findings from registry-metadata. On
	// any RPC failure buildDocument returns an error and we fail the report —
	// FUT-080: a silently-wrong compliance artifact (all-zeros) is worse than
	// a failed one an operator can retry.
	doc, err := w.buildDocument(ctx, rec.TenantID.String())
	if err != nil {
		return fmt.Errorf("build report document: %w", err)
	}

	sbom, err := report.RenderSBOM(doc)
	if err != nil {
		return fmt.Errorf("render sbom: %w", err)
	}
	pdf, err := report.RenderPDF(doc, sbom)
	if err != nil {
		return fmt.Errorf("render pdf: %w", err)
	}

	tenantDir, err := safeJoin(w.cfg.OutputDir, rec.TenantID.String())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(tenantDir, 0o755); err != nil {
		return fmt.Errorf("mkdir output dir: %w", err)
	}

	pdfPath, err := safeJoin(tenantDir, rec.ReportID.String()+".pdf")
	if err != nil {
		return err
	}
	sbomPath, err := safeJoin(tenantDir, rec.ReportID.String()+".spdx.json")
	if err != nil {
		return err
	}
	if err := os.WriteFile(pdfPath, pdf, 0o644); err != nil {
		return fmt.Errorf("write pdf: %w", err)
	}
	if err := os.WriteFile(sbomPath, sbom, 0o644); err != nil {
		return fmt.Errorf("write sbom: %w", err)
	}

	if err := w.repo.CompleteReport(ctx, rec.ReportID, pdfPath, sbomPath); err != nil {
		return fmt.Errorf("CompleteReport: %w", err)
	}
	return nil
}

// buildDocument assembles a report.Document for a tenant from live
// registry-metadata data: the aggregate severity summary (authoritative
// totals) plus the paginated detailed CVE list (capped at cfg.MaxFindings).
// Any RPC error is returned so the caller fails the report rather than
// emitting a report that under-reports the tenant's real posture (FUT-080).
func (w *Worker) buildDocument(ctx context.Context, tenantID string) (report.Document, error) {
	doc := report.Document{
		TenantID:    tenantID,
		GeneratedAt: time.Now().UTC(),
	}

	// Authoritative severity summary from the aggregate overview RPC.
	overview, err := func() (*metadatav1.SecurityOverview, error) {
		rpcCtx, cancel := context.WithTimeout(ctx, w.cfg.RPCTimeout)
		defer cancel()
		return w.meta.GetSecurityOverview(rpcCtx, &metadatav1.GetSecurityOverviewRequest{TenantId: tenantID})
	}()
	if err != nil {
		return report.Document{}, fmt.Errorf("get security overview: %w", err)
	}
	c := overview.GetSeverityCounts()
	doc.SummaryCount = map[string]int{
		"CRITICAL":   int(c.GetCritical()),
		"HIGH":       int(c.GetHigh()),
		"MEDIUM":     int(c.GetMedium()),
		"LOW":        int(c.GetLow()),
		"NEGLIGIBLE": int(c.GetNegligible()),
	}

	// Detailed findings, following pagination until exhausted or capped.
	pageToken := ""
	for {
		resp, err := func() (*metadatav1.ListTenantVulnerabilitiesResponse, error) {
			rpcCtx, cancel := context.WithTimeout(ctx, w.cfg.RPCTimeout)
			defer cancel()
			return w.meta.ListTenantVulnerabilities(rpcCtx, &metadatav1.ListTenantVulnerabilitiesRequest{
				TenantId:  tenantID,
				PageToken: pageToken,
				PageSize:  findingsPageSize,
			})
		}()
		if err != nil {
			return report.Document{}, fmt.Errorf("list tenant vulnerabilities: %w", err)
		}
		for _, v := range resp.GetVulnerabilities() {
			doc.Findings = append(doc.Findings, report.Finding{
				CVEID:       v.GetCveId(),
				Severity:    v.GetSeverity(),
				PackageName: v.GetPackageName(),
				Version:     v.GetPackageVersion(),
				FixedIn:     v.GetFixedIn(),
			})
		}
		// Defensive: an empty page contributes nothing, so stop rather than
		// risk looping forever on a server that keeps handing back a
		// non-empty page_token with no rows.
		if len(resp.GetVulnerabilities()) == 0 {
			break
		}
		if len(doc.Findings) >= w.cfg.MaxFindings {
			// Trim to the cap and stop; the summary counts remain the true
			// totals, so a truncated detail list never understates severity.
			if len(doc.Findings) > w.cfg.MaxFindings {
				doc.Findings = doc.Findings[:w.cfg.MaxFindings]
			}
			slog.Warn("compliance report findings truncated at cap",
				"tenant_id", tenantID, "cap", w.cfg.MaxFindings)
			break
		}
		pageToken = resp.GetNextPageToken()
		if pageToken == "" {
			break
		}
	}

	return doc, nil
}

// safeJoin is filepath.Join + a guard that the result stays under root.
// We reject any element containing path separators or `..` segments before
// the join so a malformed tenant_id (never produced by valid UUIDs) cannot
// escape OutputDir.
func safeJoin(root, elem string) (string, error) {
	if strings.Contains(elem, "/") || strings.Contains(elem, "\\") || strings.Contains(elem, "..") {
		return "", fmt.Errorf("unsafe path element: %q", elem)
	}
	joined := filepath.Clean(filepath.Join(root, elem))
	cleanRoot := filepath.Clean(root)
	if !strings.HasPrefix(joined, cleanRoot) {
		return "", fmt.Errorf("path %q escapes root %q", joined, cleanRoot)
	}
	return joined, nil
}

// ValidateUUID is a small helper kept here so callers (handlers + worker
// instantiation) get a consistent answer about whether a tenant_id string
// is well-formed.
func ValidateUUID(s string) error {
	_, err := uuid.Parse(s)
	return err
}

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

	"github.com/steveokay/oci-janus/services/scanner/internal/report"
	"github.com/steveokay/oci-janus/services/scanner/internal/repository"
)

// Config tunes the worker.
type Config struct {
	OutputDir    string
	PollInterval time.Duration
}

// Worker runs the report-generation loop. It is safe to run as many copies
// of Worker as there are scanner replicas — the DB poller arbitrates via
// FOR UPDATE SKIP LOCKED.
type Worker struct {
	repo *repository.Repository
	cfg  Config
}

// New constructs a Worker. The caller is responsible for ensuring OutputDir
// is creatable (the worker will MkdirAll under it).
func New(repo *repository.Repository, cfg Config) *Worker {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}
	return &Worker{repo: repo, cfg: cfg}
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
	doc := report.Document{
		TenantID:    rec.TenantID.String(),
		GeneratedAt: time.Now().UTC(),
		// v1 placeholder summary so the rendered output isn't completely
		// empty when the scanner can't reach metadata. Filling this in
		// requires a metadata gRPC client which is wired in main; the
		// worker accepts the fallback to keep the cross-service path
		// optional.
		SummaryCount: map[string]int{
			"CRITICAL": 0, "HIGH": 0, "MEDIUM": 0, "LOW": 0, "NEGLIGIBLE": 0,
		},
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

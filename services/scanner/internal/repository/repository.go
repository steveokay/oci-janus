// Package repository owns every SQL access path for the scanner service's
// own Postgres schema (scan_policies + compliance_reports).
//
// All queries are parameterised — no dynamic SQL string building. Tenant
// isolation is enforced at the application layer here; downstream RLS is a
// later hardening step. Callers must always pass the caller's tenant_id and
// must never accept a tenant_id from request bodies (see CLAUDE.md §9).
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when no row matches the lookup. Callers should
// map it to NOT_FOUND on the gRPC layer (or 404 over HTTP).
var ErrNotFound = errors.New("not found")

// ScanPolicy mirrors the scan_policies row. It is also the input shape used
// by UpsertScanPolicy — fields are validated by the gRPC handler before
// they reach the repository.
//
// FE-API-049 extension: the same struct is reused for the org_scan_policies
// and repo_scan_policies tables, with OrgID / RepoID populated as
// appropriate. Tenant-level policies leave both empty. The Enabled field
// only applies to org/repo rows — the per-tenant table doesn't carry it
// (treated as always-enabled for backward compatibility).
type ScanPolicy struct {
	TenantID          uuid.UUID
	OrgID             uuid.UUID
	RepoID            uuid.UUID
	AutoScanOnPush    bool
	BlockOnSeverity   string
	ExemptCVEs        []string
	ScannerPlugin     string
	ScannerVersionPin string
	Enabled           bool
	UpdatedAt         time.Time
	// UpdatedBy is the user_id of the last actor. Zero UUID is treated as
	// "system / default" in callers.
	UpdatedBy uuid.UUID
}

// ComplianceReport mirrors the compliance_reports row. Times that are nil
// in the underlying nullable columns surface as zero values here; callers
// branch on Status (not on a non-zero timestamp) to know whether a job
// has started or finished.
type ComplianceReport struct {
	ReportID     uuid.UUID
	TenantID     uuid.UUID
	RequestedBy  uuid.UUID
	RequestedAt  time.Time
	StartedAt    time.Time
	CompletedAt  time.Time
	Status       string
	ErrorMessage string
	PDFPath      string
	SBOMPath     string
}

// Repository owns the connection pool and is the only type that issues SQL
// against the scanner DB.
type Repository struct {
	pool *pgxpool.Pool
}

// New returns a Repository backed by the given pool.
func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// ---------------------------------------------------------------------------
// scan_policies
// ---------------------------------------------------------------------------

// GetScanPolicy returns the row for tenantID or ErrNotFound when none exists.
func (r *Repository) GetScanPolicy(ctx context.Context, tenantID uuid.UUID) (*ScanPolicy, error) {
	var p ScanPolicy
	// updated_by is nullable in the table — use COALESCE so the Scan target
	// is never NULL. Zero UUID is a valid sentinel for "no actor".
	err := r.pool.QueryRow(ctx,
		`SELECT tenant_id, auto_scan_on_push, block_on_severity,
		        exempt_cves, scanner_plugin, scanner_version_pin,
		        updated_at, COALESCE(updated_by, '00000000-0000-0000-0000-000000000000'::uuid)
		   FROM scan_policies
		  WHERE tenant_id = $1`,
		tenantID,
	).Scan(&p.TenantID, &p.AutoScanOnPush, &p.BlockOnSeverity,
		&p.ExemptCVEs, &p.ScannerPlugin, &p.ScannerVersionPin,
		&p.UpdatedAt, &p.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("GetScanPolicy: %w", err)
	}
	return &p, nil
}

// UpsertScanPolicy inserts or updates the row for the policy's tenant. The
// returned policy reflects the persisted state including updated_at.
func (r *Repository) UpsertScanPolicy(ctx context.Context, p *ScanPolicy) (*ScanPolicy, error) {
	var updatedBy any
	if p.UpdatedBy != uuid.Nil {
		updatedBy = p.UpdatedBy
	}
	var out ScanPolicy
	// ON CONFLICT updates every field — there is no field-level "leave alone"
	// for scan policy mutations; the BFF always sends the full policy state.
	err := r.pool.QueryRow(ctx,
		`INSERT INTO scan_policies
		   (tenant_id, auto_scan_on_push, block_on_severity, exempt_cves,
		    scanner_plugin, scanner_version_pin, updated_at, updated_by)
		 VALUES ($1, $2, $3, $4, $5, $6, NOW(), $7)
		 ON CONFLICT (tenant_id) DO UPDATE SET
		    auto_scan_on_push   = EXCLUDED.auto_scan_on_push,
		    block_on_severity   = EXCLUDED.block_on_severity,
		    exempt_cves         = EXCLUDED.exempt_cves,
		    scanner_plugin      = EXCLUDED.scanner_plugin,
		    scanner_version_pin = EXCLUDED.scanner_version_pin,
		    updated_at          = NOW(),
		    updated_by          = EXCLUDED.updated_by
		 RETURNING tenant_id, auto_scan_on_push, block_on_severity,
		           exempt_cves, scanner_plugin, scanner_version_pin,
		           updated_at, COALESCE(updated_by, '00000000-0000-0000-0000-000000000000'::uuid)`,
		p.TenantID, p.AutoScanOnPush, p.BlockOnSeverity, p.ExemptCVEs,
		p.ScannerPlugin, p.ScannerVersionPin, updatedBy,
	).Scan(&out.TenantID, &out.AutoScanOnPush, &out.BlockOnSeverity,
		&out.ExemptCVEs, &out.ScannerPlugin, &out.ScannerVersionPin,
		&out.UpdatedAt, &out.UpdatedBy)
	if err != nil {
		return nil, fmt.Errorf("UpsertScanPolicy: %w", err)
	}
	return &out, nil
}

// ---------------------------------------------------------------------------
// FE-API-049 — org + repo scan policies
//
// Three new access paths mirroring the per-tenant ones above. Schemas
// are structurally identical so the column lists could be shared via a
// constant; we keep them inline to make grep-by-table trivial.
// ---------------------------------------------------------------------------

// GetOrgScanPolicy returns the org-default row or ErrNotFound. tenantID
// is required even though org_id is the PK — we use it as a scoping
// predicate so a malformed org_id from another tenant can't surface
// another tenant's policy (defence-in-depth alongside the metadata
// service's tenant-on-org constraint).
func (r *Repository) GetOrgScanPolicy(ctx context.Context, tenantID, orgID uuid.UUID) (*ScanPolicy, error) {
	var p ScanPolicy
	err := r.pool.QueryRow(ctx,
		`SELECT tenant_id, org_id, auto_scan_on_push, block_on_severity,
		        exempt_cves, scanner_plugin, scanner_version_pin, enabled,
		        updated_at, COALESCE(updated_by, '00000000-0000-0000-0000-000000000000'::uuid)
		   FROM org_scan_policies
		  WHERE org_id = $1 AND tenant_id = $2`,
		orgID, tenantID,
	).Scan(&p.TenantID, &p.OrgID, &p.AutoScanOnPush, &p.BlockOnSeverity,
		&p.ExemptCVEs, &p.ScannerPlugin, &p.ScannerVersionPin, &p.Enabled,
		&p.UpdatedAt, &p.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("GetOrgScanPolicy: %w", err)
	}
	return &p, nil
}

// UpsertOrgScanPolicy creates or replaces the org-default row.
func (r *Repository) UpsertOrgScanPolicy(ctx context.Context, p *ScanPolicy) (*ScanPolicy, error) {
	var updatedBy any
	if p.UpdatedBy != uuid.Nil {
		updatedBy = p.UpdatedBy
	}
	var out ScanPolicy
	err := r.pool.QueryRow(ctx,
		`INSERT INTO org_scan_policies
		   (org_id, tenant_id, auto_scan_on_push, block_on_severity, exempt_cves,
		    scanner_plugin, scanner_version_pin, enabled, updated_at, updated_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW(), $9)
		 ON CONFLICT (org_id) DO UPDATE SET
		    auto_scan_on_push   = EXCLUDED.auto_scan_on_push,
		    block_on_severity   = EXCLUDED.block_on_severity,
		    exempt_cves         = EXCLUDED.exempt_cves,
		    scanner_plugin      = EXCLUDED.scanner_plugin,
		    scanner_version_pin = EXCLUDED.scanner_version_pin,
		    enabled             = EXCLUDED.enabled,
		    updated_at          = NOW(),
		    updated_by          = EXCLUDED.updated_by
		 RETURNING tenant_id, org_id, auto_scan_on_push, block_on_severity,
		           exempt_cves, scanner_plugin, scanner_version_pin, enabled,
		           updated_at, COALESCE(updated_by, '00000000-0000-0000-0000-000000000000'::uuid)`,
		p.OrgID, p.TenantID, p.AutoScanOnPush, p.BlockOnSeverity, p.ExemptCVEs,
		p.ScannerPlugin, p.ScannerVersionPin, p.Enabled, updatedBy,
	).Scan(&out.TenantID, &out.OrgID, &out.AutoScanOnPush, &out.BlockOnSeverity,
		&out.ExemptCVEs, &out.ScannerPlugin, &out.ScannerVersionPin, &out.Enabled,
		&out.UpdatedAt, &out.UpdatedBy)
	if err != nil {
		return nil, fmt.Errorf("UpsertOrgScanPolicy: %w", err)
	}
	return &out, nil
}

// DeleteOrgScanPolicy removes the row or returns ErrNotFound when there
// is nothing to delete. Non-idempotent on purpose so callers know
// whether they actually cleared an override.
func (r *Repository) DeleteOrgScanPolicy(ctx context.Context, tenantID, orgID uuid.UUID) error {
	cmd, err := r.pool.Exec(ctx,
		`DELETE FROM org_scan_policies WHERE org_id = $1 AND tenant_id = $2`,
		orgID, tenantID)
	if err != nil {
		return fmt.Errorf("DeleteOrgScanPolicy: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetRepoScanPolicy returns the per-repo override or ErrNotFound.
func (r *Repository) GetRepoScanPolicy(ctx context.Context, tenantID, repoID uuid.UUID) (*ScanPolicy, error) {
	var p ScanPolicy
	err := r.pool.QueryRow(ctx,
		`SELECT tenant_id, org_id, repo_id, auto_scan_on_push, block_on_severity,
		        exempt_cves, scanner_plugin, scanner_version_pin, enabled,
		        updated_at, COALESCE(updated_by, '00000000-0000-0000-0000-000000000000'::uuid)
		   FROM repo_scan_policies
		  WHERE repo_id = $1 AND tenant_id = $2`,
		repoID, tenantID,
	).Scan(&p.TenantID, &p.OrgID, &p.RepoID, &p.AutoScanOnPush, &p.BlockOnSeverity,
		&p.ExemptCVEs, &p.ScannerPlugin, &p.ScannerVersionPin, &p.Enabled,
		&p.UpdatedAt, &p.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("GetRepoScanPolicy: %w", err)
	}
	return &p, nil
}

// UpsertRepoScanPolicy creates or replaces a per-repo override.
func (r *Repository) UpsertRepoScanPolicy(ctx context.Context, p *ScanPolicy) (*ScanPolicy, error) {
	var updatedBy any
	if p.UpdatedBy != uuid.Nil {
		updatedBy = p.UpdatedBy
	}
	var out ScanPolicy
	err := r.pool.QueryRow(ctx,
		`INSERT INTO repo_scan_policies
		   (repo_id, tenant_id, org_id, auto_scan_on_push, block_on_severity, exempt_cves,
		    scanner_plugin, scanner_version_pin, enabled, updated_at, updated_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW(), $10)
		 ON CONFLICT (repo_id) DO UPDATE SET
		    org_id              = EXCLUDED.org_id,
		    auto_scan_on_push   = EXCLUDED.auto_scan_on_push,
		    block_on_severity   = EXCLUDED.block_on_severity,
		    exempt_cves         = EXCLUDED.exempt_cves,
		    scanner_plugin      = EXCLUDED.scanner_plugin,
		    scanner_version_pin = EXCLUDED.scanner_version_pin,
		    enabled             = EXCLUDED.enabled,
		    updated_at          = NOW(),
		    updated_by          = EXCLUDED.updated_by
		 RETURNING tenant_id, org_id, repo_id, auto_scan_on_push, block_on_severity,
		           exempt_cves, scanner_plugin, scanner_version_pin, enabled,
		           updated_at, COALESCE(updated_by, '00000000-0000-0000-0000-000000000000'::uuid)`,
		p.RepoID, p.TenantID, p.OrgID, p.AutoScanOnPush, p.BlockOnSeverity, p.ExemptCVEs,
		p.ScannerPlugin, p.ScannerVersionPin, p.Enabled, updatedBy,
	).Scan(&out.TenantID, &out.OrgID, &out.RepoID, &out.AutoScanOnPush, &out.BlockOnSeverity,
		&out.ExemptCVEs, &out.ScannerPlugin, &out.ScannerVersionPin, &out.Enabled,
		&out.UpdatedAt, &out.UpdatedBy)
	if err != nil {
		return nil, fmt.Errorf("UpsertRepoScanPolicy: %w", err)
	}
	return &out, nil
}

// DeleteRepoScanPolicy removes a per-repo override. ErrNotFound when no
// row existed so the BFF can render a clean 404.
func (r *Repository) DeleteRepoScanPolicy(ctx context.Context, tenantID, repoID uuid.UUID) error {
	cmd, err := r.pool.Exec(ctx,
		`DELETE FROM repo_scan_policies WHERE repo_id = $1 AND tenant_id = $2`,
		repoID, tenantID)
	if err != nil {
		return fmt.Errorf("DeleteRepoScanPolicy: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ResolveRepoOrgID returns the org_id stored on a per-repo override row,
// or ErrNotFound when no override exists. Used by GetEffectiveScanPolicy
// when the caller didn't supply org_id and there's no per-repo override
// (in which case we fall back through to tenant + default).
func (r *Repository) ResolveRepoOrgID(ctx context.Context, tenantID, repoID uuid.UUID) (uuid.UUID, error) {
	var orgID uuid.UUID
	err := r.pool.QueryRow(ctx,
		`SELECT org_id FROM repo_scan_policies WHERE repo_id = $1 AND tenant_id = $2`,
		repoID, tenantID,
	).Scan(&orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("ResolveRepoOrgID: %w", err)
	}
	return orgID, nil
}

// ---------------------------------------------------------------------------
// FUT-017 — proxy_cache_scan_policies
//
// Per-(tenant, upstream_name) gate for auto-scanning manifests freshly
// cached by services/proxy. Schema lives in the 20260624 migration; row
// shape mirrors the FE-API-018 scan_policies row minus exempt_cves /
// scanner_plugin / scanner_version_pin since proxy-cached scans piggy-
// back on the per-tenant policy's plugin selection.
// ---------------------------------------------------------------------------

// ProxyCacheScanPolicy mirrors one proxy_cache_scan_policies row. It is
// also the input shape for UpsertProxyCacheScanPolicy — the gRPC handler
// validates upstream_name + severity_threshold before reaching here.
type ProxyCacheScanPolicy struct {
	TenantID          uuid.UUID
	UpstreamName      string
	AutoScan          bool
	SeverityThreshold string
	UpdatedAt         time.Time
	UpdatedBy         uuid.UUID
}

// GetProxyCacheScanPolicy returns the row for (tenantID, upstreamName)
// or ErrNotFound. Callers (handler.GetProxyCacheScanPolicy) translate
// the not-found into a default-disabled response — the gRPC surface
// itself never raises NotFound for this lookup.
func (r *Repository) GetProxyCacheScanPolicy(ctx context.Context, tenantID uuid.UUID, upstreamName string) (*ProxyCacheScanPolicy, error) {
	var p ProxyCacheScanPolicy
	err := r.pool.QueryRow(ctx,
		`SELECT tenant_id, upstream_name, auto_scan, severity_threshold,
		        updated_at, COALESCE(updated_by, '00000000-0000-0000-0000-000000000000'::uuid)
		   FROM proxy_cache_scan_policies
		  WHERE tenant_id = $1 AND upstream_name = $2`,
		tenantID, upstreamName,
	).Scan(&p.TenantID, &p.UpstreamName, &p.AutoScan, &p.SeverityThreshold,
		&p.UpdatedAt, &p.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("GetProxyCacheScanPolicy: %w", err)
	}
	return &p, nil
}

// UpsertProxyCacheScanPolicy creates or replaces the row identified by
// (tenant_id, upstream_name). The full row is overwritten on every call
// — partial updates are out of scope (the BFF always sends the full
// policy state, same posture as UpsertScanPolicy).
func (r *Repository) UpsertProxyCacheScanPolicy(ctx context.Context, p *ProxyCacheScanPolicy) (*ProxyCacheScanPolicy, error) {
	var updatedBy any
	if p.UpdatedBy != uuid.Nil {
		updatedBy = p.UpdatedBy
	}
	var out ProxyCacheScanPolicy
	err := r.pool.QueryRow(ctx,
		`INSERT INTO proxy_cache_scan_policies
		   (tenant_id, upstream_name, auto_scan, severity_threshold,
		    updated_at, updated_by)
		 VALUES ($1, $2, $3, $4, NOW(), $5)
		 ON CONFLICT (tenant_id, upstream_name) DO UPDATE SET
		    auto_scan          = EXCLUDED.auto_scan,
		    severity_threshold = EXCLUDED.severity_threshold,
		    updated_at         = NOW(),
		    updated_by         = EXCLUDED.updated_by
		 RETURNING tenant_id, upstream_name, auto_scan, severity_threshold,
		           updated_at, COALESCE(updated_by, '00000000-0000-0000-0000-000000000000'::uuid)`,
		p.TenantID, p.UpstreamName, p.AutoScan, p.SeverityThreshold, updatedBy,
	).Scan(&out.TenantID, &out.UpstreamName, &out.AutoScan, &out.SeverityThreshold,
		&out.UpdatedAt, &out.UpdatedBy)
	if err != nil {
		return nil, fmt.Errorf("UpsertProxyCacheScanPolicy: %w", err)
	}
	return &out, nil
}

// ListProxyCacheScanPolicies returns every row for tenantID ordered by
// upstream_name ASC so the FE can render a stable table without
// client-side sorting. Returns a nil slice (not an error) when the
// tenant has no rows yet.
func (r *Repository) ListProxyCacheScanPolicies(ctx context.Context, tenantID uuid.UUID) ([]*ProxyCacheScanPolicy, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT tenant_id, upstream_name, auto_scan, severity_threshold,
		        updated_at, COALESCE(updated_by, '00000000-0000-0000-0000-000000000000'::uuid)
		   FROM proxy_cache_scan_policies
		  WHERE tenant_id = $1
		  ORDER BY upstream_name ASC`,
		tenantID,
	)
	if err != nil {
		return nil, fmt.Errorf("ListProxyCacheScanPolicies: %w", err)
	}
	defer rows.Close()
	var out []*ProxyCacheScanPolicy
	for rows.Next() {
		var p ProxyCacheScanPolicy
		if scanErr := rows.Scan(&p.TenantID, &p.UpstreamName, &p.AutoScan,
			&p.SeverityThreshold, &p.UpdatedAt, &p.UpdatedBy); scanErr != nil {
			return nil, fmt.Errorf("ListProxyCacheScanPolicies scan: %w", scanErr)
		}
		out = append(out, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// compliance_reports
// ---------------------------------------------------------------------------

// CreateReport inserts a new report row in `pending` state and returns the
// hydrated record. The caller assigns the report_id (UUIDv4) so the API can
// return it without a round-trip.
func (r *Repository) CreateReport(ctx context.Context, reportID, tenantID, requestedBy uuid.UUID) (*ComplianceReport, error) {
	var rec ComplianceReport
	err := r.pool.QueryRow(ctx,
		`INSERT INTO compliance_reports (report_id, tenant_id, requested_by)
		 VALUES ($1, $2, $3)
		 RETURNING report_id, tenant_id, requested_by, requested_at,
		           COALESCE(started_at, 'epoch'::timestamptz),
		           COALESCE(completed_at, 'epoch'::timestamptz),
		           status::TEXT, COALESCE(error_message,''),
		           COALESCE(pdf_path,''), COALESCE(sbom_path,'')`,
		reportID, tenantID, requestedBy,
	).Scan(&rec.ReportID, &rec.TenantID, &rec.RequestedBy, &rec.RequestedAt,
		&rec.StartedAt, &rec.CompletedAt, &rec.Status, &rec.ErrorMessage,
		&rec.PDFPath, &rec.SBOMPath)
	if err != nil {
		return nil, fmt.Errorf("CreateReport: %w", err)
	}
	return &rec, nil
}

// GetReport returns one row by id, enforcing tenant isolation. A row that
// exists under a different tenant surfaces as ErrNotFound so the caller
// cannot probe for the existence of other tenants' report IDs.
func (r *Repository) GetReport(ctx context.Context, reportID, tenantID uuid.UUID) (*ComplianceReport, error) {
	var rec ComplianceReport
	err := r.pool.QueryRow(ctx,
		`SELECT report_id, tenant_id, requested_by, requested_at,
		        COALESCE(started_at, 'epoch'::timestamptz),
		        COALESCE(completed_at, 'epoch'::timestamptz),
		        status::TEXT, COALESCE(error_message,''),
		        COALESCE(pdf_path,''), COALESCE(sbom_path,'')
		   FROM compliance_reports
		  WHERE report_id = $1 AND tenant_id = $2`,
		reportID, tenantID,
	).Scan(&rec.ReportID, &rec.TenantID, &rec.RequestedBy, &rec.RequestedAt,
		&rec.StartedAt, &rec.CompletedAt, &rec.Status, &rec.ErrorMessage,
		&rec.PDFPath, &rec.SBOMPath)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("GetReport: %w", err)
	}
	return &rec, nil
}

// ListReports returns recent rows for tenantID, optionally filtered by
// status. Pagination is keyset on requested_at desc — pageToken is the
// RFC3339Nano timestamp of the last row from the previous page.
func (r *Repository) ListReports(ctx context.Context, tenantID uuid.UUID, statusFilter string, limit int, pageToken string) ([]*ComplianceReport, string, error) {
	// since carries the cursor; empty string => "no cursor, start at top".
	var since any
	if pageToken != "" {
		t, err := time.Parse(time.RFC3339Nano, pageToken)
		if err != nil {
			return nil, "", fmt.Errorf("invalid page_token: %w", err)
		}
		since = t
	}
	// statusFilter is either empty (all statuses) or one of the enum
	// values. When empty we pass NULL so the WHERE clause short-circuits.
	var status any
	if statusFilter != "" {
		status = statusFilter
	}

	rows, err := r.pool.Query(ctx,
		`SELECT report_id, tenant_id, requested_by, requested_at,
		        COALESCE(started_at, 'epoch'::timestamptz),
		        COALESCE(completed_at, 'epoch'::timestamptz),
		        status::TEXT, COALESCE(error_message,''),
		        COALESCE(pdf_path,''), COALESCE(sbom_path,'')
		   FROM compliance_reports
		  WHERE tenant_id = $1
		    AND ($2::TEXT IS NULL OR status::TEXT = $2)
		    AND ($3::TIMESTAMPTZ IS NULL OR requested_at < $3)
		  ORDER BY requested_at DESC
		  LIMIT $4`,
		tenantID, status, since, limit,
	)
	if err != nil {
		return nil, "", fmt.Errorf("ListReports: %w", err)
	}
	defer rows.Close()

	var out []*ComplianceReport
	for rows.Next() {
		var rec ComplianceReport
		if scanErr := rows.Scan(&rec.ReportID, &rec.TenantID, &rec.RequestedBy, &rec.RequestedAt,
			&rec.StartedAt, &rec.CompletedAt, &rec.Status, &rec.ErrorMessage,
			&rec.PDFPath, &rec.SBOMPath); scanErr != nil {
			return nil, "", fmt.Errorf("ListReports scan: %w", scanErr)
		}
		out = append(out, &rec)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	// next_page_token is the requested_at of the last row when the page is
	// full; empty when this was the last page.
	next := ""
	if len(out) == limit && len(out) > 0 {
		next = out[len(out)-1].RequestedAt.UTC().Format(time.RFC3339Nano)
	}
	return out, next, nil
}

// ClaimPendingReport picks the oldest pending report and atomically flips it
// to running. SELECT … FOR UPDATE SKIP LOCKED makes this safe to run from
// multiple scanner replicas — each worker sees a different row.
//
// Returns ErrNotFound when no pending row is available.
func (r *Repository) ClaimPendingReport(ctx context.Context) (*ComplianceReport, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("ClaimPendingReport begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var rec ComplianceReport
	err = tx.QueryRow(ctx,
		`SELECT report_id, tenant_id, requested_by, requested_at,
		        COALESCE(started_at, 'epoch'::timestamptz),
		        COALESCE(completed_at, 'epoch'::timestamptz),
		        status::TEXT, COALESCE(error_message,''),
		        COALESCE(pdf_path,''), COALESCE(sbom_path,'')
		   FROM compliance_reports
		  WHERE status = 'pending'
		  ORDER BY requested_at
		  LIMIT 1
		  FOR UPDATE SKIP LOCKED`,
	).Scan(&rec.ReportID, &rec.TenantID, &rec.RequestedBy, &rec.RequestedAt,
		&rec.StartedAt, &rec.CompletedAt, &rec.Status, &rec.ErrorMessage,
		&rec.PDFPath, &rec.SBOMPath)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("ClaimPendingReport select: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE compliance_reports
		    SET status = 'running', started_at = NOW()
		  WHERE report_id = $1`,
		rec.ReportID,
	); err != nil {
		return nil, fmt.Errorf("ClaimPendingReport update: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("ClaimPendingReport commit: %w", err)
	}
	rec.Status = "running"
	rec.StartedAt = time.Now().UTC()
	return &rec, nil
}

// CompleteReport marks a report as succeeded with output paths.
func (r *Repository) CompleteReport(ctx context.Context, reportID uuid.UUID, pdfPath, sbomPath string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE compliance_reports
		    SET status = 'succeeded', completed_at = NOW(),
		        pdf_path = $2, sbom_path = $3
		  WHERE report_id = $1`,
		reportID, pdfPath, sbomPath,
	)
	if err != nil {
		return fmt.Errorf("CompleteReport: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// FailReport marks a report as failed with an error message.
func (r *Repository) FailReport(ctx context.Context, reportID uuid.UUID, errMessage string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE compliance_reports
		    SET status = 'failed', completed_at = NOW(), error_message = $2
		  WHERE report_id = $1`,
		reportID, errMessage,
	)
	if err != nil {
		return fmt.Errorf("FailReport: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// scanner_settings — REM-011 Phase 2 active-adapter persistence
// ---------------------------------------------------------------------------

// GetActiveAdapter returns the path currently recorded as active in the
// scanner_settings singleton row, or an empty string when no row has
// been written yet. Empty string is treated by the caller as "fall back
// to SCANNER_PLUGIN_PATH" rather than an error — a fresh deployment has
// never had a SetActiveAdapter call and that is normal.
func (r *Repository) GetActiveAdapter(ctx context.Context) (string, error) {
	var path string
	err := r.pool.QueryRow(ctx,
		`SELECT active_adapter_path FROM scanner_settings WHERE singleton = TRUE`,
	).Scan(&path)
	if errors.Is(err, pgx.ErrNoRows) {
		// "no row" is a valid state on a fresh DB; return empty so the
		// caller can fall back to the env-var default.
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("GetActiveAdapter: %w", err)
	}
	return path, nil
}

// SetActiveAdapter upserts the scanner_settings singleton row with the
// given path. actor is recorded verbatim — pass the caller's user_id
// (a UUID string) when the change comes from a SetActiveAdapter RPC, or
// the literal "system" when applying a startup default.
//
// The single-row invariant is enforced by the table's PK (singleton
// fixed TRUE) so ON CONFLICT (singleton) always hits the existing row.
func (r *Repository) SetActiveAdapter(ctx context.Context, path, actor string) error {
	if path == "" {
		// The migration's NOT NULL constraint would catch this, but
		// failing here gives a cleaner error message.
		return fmt.Errorf("SetActiveAdapter: path must not be empty")
	}
	if actor == "" {
		actor = "system"
	}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO scanner_settings (singleton, active_adapter_path, updated_at, updated_by)
		 VALUES (TRUE, $1, NOW(), $2)
		 ON CONFLICT (singleton) DO UPDATE SET
		    active_adapter_path = EXCLUDED.active_adapter_path,
		    updated_at          = NOW(),
		    updated_by          = EXCLUDED.updated_by`,
		path, actor,
	)
	if err != nil {
		return fmt.Errorf("SetActiveAdapter: %w", err)
	}
	return nil
}

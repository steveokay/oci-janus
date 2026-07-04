// Package repository owns every SQL access path for the gc service's
// own Postgres schema (gc_runs). All queries are parameterised — no
// dynamic SQL string building.
//
// FE-API-032 introduced this package. Before it the gc service was
// completely stateless; the cron loop logged sweep results to slog and
// emitted RabbitMQ events but nothing was durable. The dashboard
// couldn't render "when did the last sweep run? how much did it free?"
// without a place to record per-run history.
package repository

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when no row matches a lookup. Callers map it
// to NOT_FOUND at the gRPC layer.
var ErrNotFound = errors.New("not found")

// GCRun mirrors the gc_runs table row. Nullable columns surface as zero
// values here; callers branch on Status (not on a non-zero timestamp)
// to know whether a sweep is queued, running, or finished.
//
// FE-API-040: retention modes piggyback on the same row by repurposing
// existing columns. `manifests_deleted` carries:
//   - the soft-delete count when mode = retention (matches "manifests
//     marked for retention");
//   - the hard-delete count when mode = retention_grace.
//
// The split is cosmetic — the retention summary RPC re-labels the values
// when surfacing them to the client. RepoID is added so the per-repo
// TriggerRetentionRun can record the target without inventing a separate
// table.
type GCRun struct {
	RunID uuid.UUID
	// TenantID is uuid.Nil for cross-tenant cron sweeps.
	TenantID uuid.UUID
	// RepoID is uuid.Nil for tenant-wide / cross-tenant sweeps. Populated
	// for retention runs (which are always repo-scoped).
	RepoID           uuid.UUID
	Mode             string
	Status           string
	RequestedAt      time.Time
	StartedAt        time.Time
	CompletedAt      time.Time
	DurationMS       int64
	BlobsFreed       int64
	ManifestsDeleted int64
	BytesFreed       int64
	ErrorMessage     string
	TriggeredBy      string
}

// Repository owns the connection pool and is the only type that issues
// SQL against the gc_runs table.
type Repository struct {
	pool *pgxpool.Pool
}

// New returns a Repository backed by the given pool.
func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// nullableTenant turns uuid.Nil into a SQL NULL so cross-tenant rows
// land with NULL in tenant_id rather than the zero UUID literal (which
// would otherwise inflate uniqueness queries and FK joins).
func nullableTenant(t uuid.UUID) any {
	if t == uuid.Nil {
		return nil
	}
	return t
}

// CreateRun inserts a fresh row in `queued` state and returns the
// hydrated record. Used by RunNow on every manual trigger.
//
// triggeredBy is recorded verbatim — `cron` for scheduled sweeps, a
// stringified UUID for user-driven runs.
func (r *Repository) CreateRun(ctx context.Context, mode string, tenantID uuid.UUID, triggeredBy string) (*GCRun, error) {
	return r.createRun(ctx, mode, tenantID, uuid.Nil, triggeredBy)
}

// CreateRetentionRun is the repo-scoped sibling of CreateRun used by the
// FE-API-040 retention executor. Pre-fills repo_id so GetRunByID can echo
// the target back without a JOIN against metadata.
func (r *Repository) CreateRetentionRun(ctx context.Context, mode string, tenantID, repoID uuid.UUID, triggeredBy string) (*GCRun, error) {
	return r.createRun(ctx, mode, tenantID, repoID, triggeredBy)
}

// createRun is the shared INSERT used by both CreateRun (legacy tenant-wide
// sweeps) and CreateRetentionRun (FE-API-040 repo-scoped sweeps). Splitting
// the exported callers from the private helper keeps the call sites clear
// about whether they're in retention land or general GC land.
func (r *Repository) createRun(ctx context.Context, mode string, tenantID, repoID uuid.UUID, triggeredBy string) (*GCRun, error) {
	id := uuid.New()
	row, err := r.scanOne(ctx,
		`INSERT INTO gc_runs (run_id, tenant_id, repo_id, mode, status, triggered_by)
		 VALUES ($1, $2, $3, $4::gc_run_mode, 'queued', $5)
		 RETURNING `+selectColumns,
		id, nullableTenant(tenantID), nullableTenant(repoID), mode, triggeredBy,
	)
	if err != nil {
		return nil, fmt.Errorf("createRun: %w", err)
	}
	return row, nil
}

// StartRun flips a queued/just-created row to `running` and stamps
// started_at. Idempotent — calling on an already-running row simply
// re-records started_at, which is fine for our purposes.
func (r *Repository) StartRun(ctx context.Context, runID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE gc_runs
		    SET status = 'running', started_at = NOW()
		  WHERE run_id = $1`,
		runID,
	)
	if err != nil {
		return fmt.Errorf("StartRun: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CompleteRun marks the run as succeeded and records the sweep
// summary. duration is computed by the caller (NOW() - started_at).
func (r *Repository) CompleteRun(ctx context.Context, runID uuid.UUID, blobsFreed, manifestsDeleted, bytesFreed int64) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE gc_runs
		    SET status            = 'succeeded',
		        completed_at      = NOW(),
		        duration_ms       = COALESCE(
		                              EXTRACT(EPOCH FROM (NOW() - started_at)) * 1000,
		                              0
		                            )::BIGINT,
		        blobs_freed       = $2,
		        manifests_deleted = $3,
		        bytes_freed       = $4
		  WHERE run_id = $1`,
		runID, blobsFreed, manifestsDeleted, bytesFreed,
	)
	if err != nil {
		return fmt.Errorf("CompleteRun: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// FailRun marks the run as failed with an error message. Called from a
// deferred panic recovery so a crashing sweep still leaves a clean row.
func (r *Repository) FailRun(ctx context.Context, runID uuid.UUID, errMessage string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE gc_runs
		    SET status        = 'failed',
		        completed_at  = NOW(),
		        duration_ms   = COALESCE(
		                          EXTRACT(EPOCH FROM (NOW() - started_at)) * 1000,
		                          0
		                        )::BIGINT,
		        error_message = $2
		  WHERE run_id = $1`,
		runID, errMessage,
	)
	if err != nil {
		return fmt.Errorf("FailRun: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetLatest returns the most recent row by completed_at (NULLS LAST so
// an in-flight run sits above the historical ones). Returns
// ErrNotFound when no rows exist yet.
func (r *Repository) GetLatest(ctx context.Context) (*GCRun, error) {
	return r.scanOne(ctx,
		`SELECT `+selectColumns+`
		   FROM gc_runs
		  ORDER BY completed_at DESC NULLS LAST, requested_at DESC
		  LIMIT 1`,
	)
}

// ListRunsFilters bundles the optional row-level filters ListRuns accepts.
// Every field is optional — the zero value preserves the pre-S-MAINT-1 F2
// "no filter" semantics.
//
// S-MAINT-1 F2 (2026-06-22) added the bottom three fields (TriggeredBy +
// DateFrom + DateTo). They share the same call-site as the existing
// REM-013 fields (RepoID + Modes) but are kept on the same struct so
// future filter additions don't grow the function signature.
type ListRunsFilters struct {
	// RepoID scopes the listing to one repository. uuid.Nil = no filter.
	RepoID uuid.UUID
	// Modes restricts to specific gc_run_mode values. Empty = no filter.
	Modes []string
	// TriggeredBy is a case-insensitive substring match against
	// gc_runs.triggered_by. Empty = no filter. Useful to find "what did
	// this user run" or "everything cron did this week" by typing "cron".
	TriggeredBy string
	// DateFrom / DateTo bound the listing to runs whose anchor timestamp
	// falls in the half-open interval [DateFrom, DateTo). The anchor is
	// COALESCE(completed_at, started_at, requested_at) so an in-flight
	// queued run still falls into the right bucket. Either or both may
	// be nil to leave that side unbounded.
	DateFrom *time.Time
	DateTo   *time.Time
}

// ListRuns returns recent rows ordered by completed_at DESC NULLS LAST
// then by run_id. Pagination is keyset on the (completed_at, run_id)
// tuple, encoded as base64url(`completed_at|run_id`) to keep the cursor
// opaque to callers. completed_at uses an empty string when NULL so
// in-flight rows can still page out cleanly.
//
// Limit is enforced at [1, 200] by the caller.
//
// All filters in `filters` are optional — the zero value preserves the
// pre-S-MAINT-1 F2 "no filter" semantics. Filters are positional bind
// params so the optimiser still uses the existing
// (completed_at DESC, run_id ASC) index; each guard collapses to a
// constant TRUE when its field is unset.
func (r *Repository) ListRuns(ctx context.Context, limit int, pageToken string, filters ListRunsFilters) ([]*GCRun, string, error) {
	var sinceCompleted any
	var sinceRunID any
	if pageToken != "" {
		c, id, err := decodePageToken(pageToken)
		if err != nil {
			return nil, "", fmt.Errorf("invalid page_token: %w", err)
		}
		if c == nil {
			// nil cursor on completed_at means "the prior page ended on a
			// row whose completed_at was NULL"; advance via run_id only.
			sinceCompleted = nil
		} else {
			sinceCompleted = *c
		}
		sinceRunID = id
	}

	// REM-013 gap 2 — pass uuid.Nil through as NULL so the IS NULL guard
	// short-circuits the predicate. pgx handles uuid.UUID natively but
	// the SQL `IS NULL` check needs an actual NULL on the wire.
	var repoIDArg any
	if filters.RepoID != uuid.Nil {
		repoIDArg = filters.RepoID
	}
	// Always pass an empty slice rather than nil so cardinality($N) is
	// well-defined; pgx encodes []string as TEXT[].
	modes := filters.Modes
	if modes == nil {
		modes = []string{}
	}

	// S-MAINT-1 F2 — date_from / date_to bind as nullable timestamps; the
	// SQL guards skip the predicate when either is NULL. Triggered_by is
	// passed as plain text; the ILIKE wildcard wraps happen in SQL so an
	// empty input collapses to the no-op `'' = ''` branch.
	var dateFromArg, dateToArg any
	if filters.DateFrom != nil {
		dateFromArg = *filters.DateFrom
	}
	if filters.DateTo != nil {
		dateToArg = *filters.DateTo
	}

	// The WHERE clause implements (completed_at, run_id) < (cursor)
	// against the DESC NULLS LAST ordering. Three branches:
	//
	//   - No cursor: return everything.
	//   - Cursor on a non-NULL completed_at row: skip rows whose
	//     completed_at is greater, and rows where completed_at equals
	//     but run_id is lexically larger.
	//   - Cursor on a NULL completed_at row: only chase rows that are
	//     also NULL with a smaller run_id (NULLS LAST already handled
	//     the non-NULL ones).
	rows, err := r.pool.Query(ctx,
		`SELECT `+selectColumns+`
		   FROM gc_runs
		  WHERE
		    (
		      ($2::TIMESTAMPTZ IS NULL AND $3::UUID IS NULL)
		      OR (
		        $2::TIMESTAMPTZ IS NOT NULL
		        AND (
		          completed_at IS NULL
		          OR completed_at < $2
		          OR (completed_at = $2 AND run_id > $3)
		        )
		      )
		      OR (
		        $2::TIMESTAMPTZ IS NULL
		        AND $3::UUID IS NOT NULL
		        AND completed_at IS NULL
		        AND run_id > $3
		      )
		    )
		    AND ($4::UUID IS NULL OR repo_id = $4::UUID)
		    AND (cardinality($5::TEXT[]) = 0 OR mode::TEXT = ANY($5::TEXT[]))
		    AND ($6 = '' OR COALESCE(triggered_by, '') ILIKE '%' || $6 || '%')
		    AND ($7::TIMESTAMPTZ IS NULL OR COALESCE(completed_at, started_at, requested_at) >= $7)
		    AND ($8::TIMESTAMPTZ IS NULL OR COALESCE(completed_at, started_at, requested_at) <  $8)
		  ORDER BY completed_at DESC NULLS LAST, run_id ASC
		  LIMIT $1`,
		limit, sinceCompleted, sinceRunID, repoIDArg, modes, filters.TriggeredBy, dateFromArg, dateToArg,
	)
	if err != nil {
		return nil, "", fmt.Errorf("ListRuns: %w", err)
	}
	defer rows.Close()

	var out []*GCRun
	for rows.Next() {
		rec, err := scanRow(rows.Scan)
		if err != nil {
			return nil, "", fmt.Errorf("ListRuns scan: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	// Emit a cursor only when we filled the page — anything less means
	// we're at the end.
	next := ""
	if len(out) == limit && len(out) > 0 {
		last := out[len(out)-1]
		next = encodePageToken(last.CompletedAt, last.RunID)
	}
	return out, next, nil
}

// GetRunByID returns a specific row by id, optionally constrained to a
// tenant. tenantID == uuid.Nil skips the tenant filter (the BFF uses this
// when the caller already holds the platform-admin marker; the per-repo
// retention status RPC passes the caller's tenant explicitly).
//
// Returns ErrNotFound when no row matches.
func (r *Repository) GetRunByID(ctx context.Context, runID, tenantID uuid.UUID) (*GCRun, error) {
	if tenantID == uuid.Nil {
		return r.scanOne(ctx,
			`SELECT `+selectColumns+` FROM gc_runs WHERE run_id = $1`,
			runID,
		)
	}
	return r.scanOne(ctx,
		`SELECT `+selectColumns+` FROM gc_runs WHERE run_id = $1 AND tenant_id = $2`,
		runID, tenantID,
	)
}

// FinalizeRetentionRun records the result of a retention-mode sweep. Unlike
// CompleteRun (which surfaces blobs/manifest counts) the retention case
// only needs a marked count — both modes flow through manifests_deleted on
// the column because retention runs do not interact with blobs directly.
// (retention_grace's blob freeing piggybacks on the same field set by
// design: see the GCRun doc comment for the cosmetic mapping.)
func (r *Repository) FinalizeRetentionRun(
	ctx context.Context,
	runID uuid.UUID,
	manifestsCount, blobsFreed, bytesFreed int64,
	errMessage string,
) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE gc_runs
		    SET status            = 'succeeded',
		        completed_at      = NOW(),
		        duration_ms       = COALESCE(
		                              EXTRACT(EPOCH FROM (NOW() - started_at)) * 1000,
		                              0
		                            )::BIGINT,
		        manifests_deleted = $2,
		        blobs_freed       = $3,
		        bytes_freed       = $4,
		        error_message     = NULLIF($5, '')
		  WHERE run_id = $1`,
		runID, manifestsCount, blobsFreed, bytesFreed, errMessage,
	)
	if err != nil {
		return fmt.Errorf("FinalizeRetentionRun: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ClaimNextQueued atomically picks the oldest queued row and flips it
// to `running` so the GC dispatcher loop can hand the work to the
// collector. SELECT … FOR UPDATE SKIP LOCKED makes this safe across
// multiple gc replicas — each worker sees a different row.
//
// Returns ErrNotFound when no queued row is available, which the
// dispatcher treats as `nothing to do; wait for the next tick`.
func (r *Repository) ClaimNextQueued(ctx context.Context) (*GCRun, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("ClaimNextQueued begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rec, err := scanRow(tx.QueryRow(ctx,
		`SELECT `+selectColumns+`
		   FROM gc_runs
		  WHERE status = 'queued'
		  ORDER BY requested_at
		  LIMIT 1
		  FOR UPDATE SKIP LOCKED`,
	).Scan)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("ClaimNextQueued select: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE gc_runs
		    SET status = 'running', started_at = NOW()
		  WHERE run_id = $1`,
		rec.RunID,
	); err != nil {
		return nil, fmt.Errorf("ClaimNextQueued update: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("ClaimNextQueued commit: %w", err)
	}
	rec.Status = "running"
	rec.StartedAt = time.Now().UTC()
	return rec, nil
}

// RetentionSavings is the lifetime aggregate returned by
// GetTenantRetentionSavings. LastRunAt is nil when the tenant has never had a
// retention sweep complete.
type RetentionSavings struct {
	ReclaimedBytes   int64
	ManifestsDeleted int64
	RunCount         int64
	LastRunAt        *time.Time
}

// GetTenantRetentionSavings sums bytes_freed + manifests_deleted over the
// tenant's *succeeded* retention-mode runs, and reports how many such runs
// were counted plus when the most recent one finished. Failed/queued/running
// runs and non-retention modes ('dry-run','manifests','blobs','full') are
// excluded so the number reflects work that actually reclaimed storage.
//
// The real hard-delete (and thus the non-zero bytes_freed) happens in the
// 'retention_grace' pass, but both retention modes are summed so callers see a
// stable lifetime figure regardless of which pass a given run recorded bytes
// in. COALESCE keeps the counters at 0 (rather than NULL) when the tenant has
// no matching rows; MAX(completed_at) is left nullable and scanned into a
// *time.Time. (REM-013 gap 3.)
func (r *Repository) GetTenantRetentionSavings(ctx context.Context, tenantID uuid.UUID) (RetentionSavings, error) {
	var s RetentionSavings
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(bytes_freed), 0),
		        COALESCE(SUM(manifests_deleted), 0),
		        COUNT(*),
		        MAX(completed_at)
		   FROM gc_runs
		  WHERE tenant_id = $1
		    AND mode IN ('retention', 'retention_grace')
		    AND status = 'succeeded'`,
		tenantID,
	).Scan(&s.ReclaimedBytes, &s.ManifestsDeleted, &s.RunCount, &s.LastRunAt)
	if err != nil {
		return RetentionSavings{}, fmt.Errorf("GetTenantRetentionSavings: %w", err)
	}
	return s, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// selectColumns is the canonical column list every SELECT uses so a
// later schema migration only needs to update this constant.
//
// COALESCE on the nullable timestamps lets pgx Scan into a non-nullable
// time.Time field; callers branch on Status rather than on whether the
// time is zero. repo_id (FE-API-040) is COALESCED to the zero UUID for
// runs that pre-date the column or that don't carry a repo target.
const selectColumns = `
	run_id,
	COALESCE(tenant_id, '00000000-0000-0000-0000-000000000000'::uuid),
	COALESCE(repo_id,   '00000000-0000-0000-0000-000000000000'::uuid),
	mode::TEXT,
	status::TEXT,
	requested_at,
	COALESCE(started_at,   'epoch'::timestamptz),
	COALESCE(completed_at, 'epoch'::timestamptz),
	COALESCE(duration_ms, 0),
	blobs_freed,
	manifests_deleted,
	bytes_freed,
	COALESCE(error_message, ''),
	triggered_by`

// scanRow is the shared row decoder used by every SELECT.
func scanRow(scan func(dest ...any) error) (*GCRun, error) {
	var rec GCRun
	if err := scan(
		&rec.RunID,
		&rec.TenantID,
		&rec.RepoID,
		&rec.Mode,
		&rec.Status,
		&rec.RequestedAt,
		&rec.StartedAt,
		&rec.CompletedAt,
		&rec.DurationMS,
		&rec.BlobsFreed,
		&rec.ManifestsDeleted,
		&rec.BytesFreed,
		&rec.ErrorMessage,
		&rec.TriggeredBy,
	); err != nil {
		return nil, err
	}
	return &rec, nil
}

// scanOne wraps a single-row QueryRow + scanRow + ErrNotFound mapping.
func (r *Repository) scanOne(ctx context.Context, sql string, args ...any) (*GCRun, error) {
	rec, err := scanRow(r.pool.QueryRow(ctx, sql, args...).Scan)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return rec, nil
}

// encodePageToken serialises a (completed_at, run_id) tuple as
// base64url(`completed_at|run_id`). completed_at is the RFC3339Nano
// representation; an empty completed_at marker carries no leading
// portion so the dispatcher can chain through NULL completed_at rows
// when paging through in-flight sweeps.
func encodePageToken(completed time.Time, runID uuid.UUID) string {
	var ts string
	if !completed.IsZero() && completed.Year() > 1970 {
		ts = completed.UTC().Format(time.RFC3339Nano)
	}
	raw := ts + "|" + runID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodePageToken reverses encodePageToken. Returns (nil-time-ptr,
// run_id, nil) when the cursor refers to a NULL completed_at row.
func decodePageToken(token string) (*time.Time, uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, uuid.Nil, fmt.Errorf("decode base64: %w", err)
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return nil, uuid.Nil, errors.New("malformed token: missing separator")
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return nil, uuid.Nil, fmt.Errorf("parse run_id: %w", err)
	}
	if parts[0] == "" {
		return nil, id, nil
	}
	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return nil, uuid.Nil, fmt.Errorf("parse completed_at: %w", err)
	}
	return &ts, id, nil
}

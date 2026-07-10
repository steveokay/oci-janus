package repository

// Package repository — pr_registry.go
//
// FUT-023 Phase 1 — ephemeral PR-scoped registries. This file is the
// data-access layer for the two tables introduced in migration
// 00020_pr_registry.sql:
//
//   pr_registry_config — one row per tenant (tenant_id PK). Holds the
//   per-tenant enable flag, the KEK-sealed webhook secret, its kek_version
//   stamp, and an optional promote target org. Persisted verbatim: this
//   layer never seals/unseals — the handler does the crypto and hands us
//   the ciphertext.
//
//   pr_namespaces — one lifecycle row per PR namespace, keyed by the
//   UNIQUE (tenant_id, provider, source_repo, pr_number) composite so a
//   re-delivered webhook re-provisions idempotently rather than duplicating.
//
// Every method is tenant-scoped in its WHERE clause (CLAUDE.md §9) even
// though the platform is single-tenant today — a cross-tenant read or
// delete must never be expressible from here.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PRRegistryConfig is one pr_registry_config row.
//
// WebhookSecretEnc is the raw AES-256-GCM ciphertext (or nil when unset) —
// this layer never seals or unseals it. UpdatedBy is *uuid.UUID so a
// CLI/bot write persists NULL rather than the zero UUID; PromoteTargetOrg
// is a plain string ("" ⇔ SQL NULL) because callers only ever need the
// "is a target set" question, which "" answers cleanly.
type PRRegistryConfig struct {
	TenantID         uuid.UUID
	Enabled          bool
	WebhookSecretEnc []byte
	KEKVersion       int16
	PromoteTargetOrg string
	UpdatedAt        time.Time
	UpdatedBy        *uuid.UUID
}

// PRNamespace is one pr_namespaces lifecycle row.
//
// OrgID is *uuid.UUID because the FK is ON DELETE SET NULL — a torn-down
// namespace keeps its lifecycle row with a NULL org_id. TornDownAt is
// *time.Time and nil while the namespace is still active.
type PRNamespace struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	OrgID      *uuid.UUID
	Provider   string
	SourceRepo string
	PRNumber   int
	OrgName    string
	Status     string
	CreatedAt  time.Time
	TornDownAt *time.Time
}

// prRegistryConfigCols is the column list every pr_registry_config read
// shares. Kept as a constant so a new reader can't silently drop a field.
const prRegistryConfigCols = `tenant_id, enabled, webhook_secret_enc, kek_version,
	COALESCE(promote_target_org, ''), updated_at, updated_by`

// GetPRRegistryConfig returns the tenant's PR-registry config row, or
// ErrNotFound when the tenant has never written one. The handler maps
// ErrNotFound to a sensible "feature off / defaults" response rather than
// surfacing it as an error — absence is the normal state for a tenant that
// has never touched the feature.
func (r *Repository) GetPRRegistryConfig(ctx context.Context, tenantID uuid.UUID) (*PRRegistryConfig, error) {
	const q = `SELECT ` + prRegistryConfigCols + `
		FROM pr_registry_config WHERE tenant_id = $1`
	var cfg PRRegistryConfig
	if err := r.reader().QueryRow(ctx, q, tenantID).Scan(
		&cfg.TenantID, &cfg.Enabled, &cfg.WebhookSecretEnc, &cfg.KEKVersion,
		&cfg.PromoteTargetOrg, &cfg.UpdatedAt, &cfg.UpdatedBy,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get pr registry config: %w", err)
	}
	return &cfg, nil
}

// UpsertPRRegistryConfig inserts or replaces the tenant's config row.
//
// The handler decides keep-vs-replace for the webhook secret BEFORE calling
// (it re-passes the existing ciphertext to keep, or a fresh sealed blob to
// replace) — here we simply persist whatever WebhookSecretEnc holds, so a
// nil clears it and a non-nil sets it. promote_target_org "" persists as
// NULL via NULLIF. updated_at is always stamped now() server-side; the
// caller's value is ignored.
func (r *Repository) UpsertPRRegistryConfig(ctx context.Context, cfg PRRegistryConfig) error {
	const q = `
		INSERT INTO pr_registry_config (
			tenant_id, enabled, webhook_secret_enc, kek_version,
			promote_target_org, updated_at, updated_by
		)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), now(), $6)
		ON CONFLICT (tenant_id) DO UPDATE SET
			enabled            = EXCLUDED.enabled,
			webhook_secret_enc = EXCLUDED.webhook_secret_enc,
			kek_version        = EXCLUDED.kek_version,
			promote_target_org = EXCLUDED.promote_target_org,
			updated_at         = now(),
			updated_by         = EXCLUDED.updated_by`
	if _, err := r.pool.Exec(ctx, q,
		cfg.TenantID, cfg.Enabled, cfg.WebhookSecretEnc, cfg.KEKVersion,
		cfg.PromoteTargetOrg, cfg.UpdatedBy,
	); err != nil {
		return fmt.Errorf("upsert pr registry config: %w", err)
	}
	return nil
}

// prNamespaceCols is the column list every pr_namespaces read shares.
const prNamespaceCols = `id, tenant_id, org_id, provider, source_repo,
	pr_number, org_name, status, created_at, torn_down_at`

// scanPRNamespace decodes one pr_namespaces row. Serves both the single-row
// RETURNING/QueryRow paths and the multi-row list loop via the shared
// rowScanner interface (declared in promotions.go).
func scanPRNamespace(row rowScanner) (*PRNamespace, error) {
	var ns PRNamespace
	if err := row.Scan(
		&ns.ID, &ns.TenantID, &ns.OrgID, &ns.Provider, &ns.SourceRepo,
		&ns.PRNumber, &ns.OrgName, &ns.Status, &ns.CreatedAt, &ns.TornDownAt,
	); err != nil {
		return nil, err
	}
	return &ns, nil
}

// UpsertPRNamespace provisions (or re-provisions) the namespace for a PR.
// The UNIQUE (tenant_id, provider, source_repo, pr_number) key makes this
// idempotent: a re-delivered "PR opened" webhook re-activates the existing
// lifecycle row (status back to 'active', torn_down_at cleared, org_id +
// org_name refreshed to the new ephemeral org) rather than creating a
// duplicate. RETURNING yields the full row so the caller sees the
// server-assigned id + created_at without a second round-trip.
func (r *Repository) UpsertPRNamespace(ctx context.Context, ns PRNamespace) (*PRNamespace, error) {
	const q = `
		INSERT INTO pr_namespaces (
			tenant_id, org_id, provider, source_repo, pr_number, org_name, status
		)
		VALUES ($1, $2, $3, $4, $5, $6, 'active')
		ON CONFLICT (tenant_id, provider, source_repo, pr_number) DO UPDATE SET
			org_id       = EXCLUDED.org_id,
			org_name     = EXCLUDED.org_name,
			status       = 'active',
			torn_down_at = NULL
		RETURNING ` + prNamespaceCols
	out, err := scanPRNamespace(r.pool.QueryRow(ctx, q,
		ns.TenantID, ns.OrgID, ns.Provider, ns.SourceRepo, ns.PRNumber, ns.OrgName,
	))
	if err != nil {
		return nil, fmt.Errorf("upsert pr namespace: %w", err)
	}
	return out, nil
}

// GetPRNamespace looks up a namespace by its unique lifecycle key. Returns
// ErrNotFound when no row exists for the (tenant, provider, source_repo,
// pr_number) tuple.
func (r *Repository) GetPRNamespace(ctx context.Context, tenantID uuid.UUID, provider, sourceRepo string, prNumber int) (*PRNamespace, error) {
	const q = `SELECT ` + prNamespaceCols + `
		FROM pr_namespaces
		WHERE tenant_id = $1 AND provider = $2 AND source_repo = $3 AND pr_number = $4`
	out, err := scanPRNamespace(r.reader().QueryRow(ctx, q, tenantID, provider, sourceRepo, prNumber))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get pr namespace: %w", err)
	}
	return out, nil
}

// TearDownPRNamespace marks a PR namespace torn down and deletes its
// ephemeral org — atomically, in one transaction, so a caller never
// observes "org deleted but namespace still active" or vice versa.
//
// Both writes are scoped by tenantID (CLAUDE.md §9 / SEC-085 #3) so a
// caller can never tear down or delete another tenant's namespace/org even
// if a mismatched (namespaceID, orgID) pair is supplied — the tenant guard
// is kept even though the platform is single-tenant today.
//
// The lifecycle row is preserved (status='torn_down', torn_down_at=now(),
// org_id=NULL) so the audit/history of the PR survives the org deletion.
// The explicit org_id=NULL and the FK's ON DELETE SET NULL are belt-and-
// suspenders: either alone would null the reference, but stamping it in the
// UPDATE keeps the row correct even if the FK behaviour is ever changed.
//
// orgID may be uuid.Nil — a namespace that was already torn down (or never
// had an org) has no org to delete, so the DELETE is skipped in that case
// and only the status flip is applied. This keeps repeated teardowns
// idempotent.
func (r *Repository) TearDownPRNamespace(ctx context.Context, tenantID, namespaceID, orgID uuid.UUID) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin teardown pr namespace tx: %w", err)
	}
	// Rollback on a committed tx is a no-op (pgx documents this), so the
	// deferred rollback is safe in the success path and unwinds every write
	// on any error before Commit.
	defer func() { _ = tx.Rollback(ctx) }()

	const markQ = `
		UPDATE pr_namespaces
		SET status = 'torn_down', torn_down_at = now(), org_id = NULL
		WHERE id = $1 AND tenant_id = $2`
	if _, err := tx.Exec(ctx, markQ, namespaceID, tenantID); err != nil {
		return fmt.Errorf("mark pr namespace torn down: %w", err)
	}

	// Skip the org delete when there is no org to remove — already torn
	// down or never provisioned. The status flip above is still applied.
	if orgID != uuid.Nil {
		const delOrgQ = `DELETE FROM organizations WHERE id = $1 AND tenant_id = $2`
		if _, err := tx.Exec(ctx, delOrgQ, orgID, tenantID); err != nil {
			return fmt.Errorf("delete pr namespace org: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit teardown pr namespace tx: %w", err)
	}
	return nil
}

// DeleteOrganization deletes an org, scoped by BOTH tenant_id and id so a
// caller can never delete another tenant's org (CLAUDE.md §9 — the guard
// is kept even though the platform is single-tenant today). Backs the
// DeleteOrganization RPC. Returns ErrNotFound when no matching row exists.
func (r *Repository) DeleteOrganization(ctx context.Context, tenantID, orgID uuid.UUID) error {
	const q = `DELETE FROM organizations WHERE id = $1 AND tenant_id = $2`
	tag, err := r.pool.Exec(ctx, q, orgID, tenantID)
	if err != nil {
		return fmt.Errorf("delete organization: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// prNamespaceCursor is the base64-encoded (created_at|id) keyset cursor
// returned to callers as next_page_token. Mirrors scanHistoryCursor
// (vulnerabilities.go): RFC3339Nano timestamp so the token is human-
// readable when base64-decoded during debugging, plus the row UUID as a
// deterministic tie-break. URL-safe base64 so it passes verbatim in a URL.
type prNamespaceCursor struct {
	CreatedAt time.Time
	ID        string
}

// encodePRNamespaceCursor base64-encodes (created_at|id). The pipe
// separator is safe: RFC3339Nano timestamps cannot contain "|" and id is a
// UUID.
func encodePRNamespaceCursor(c prNamespaceCursor) string {
	raw := c.CreatedAt.Format(time.RFC3339Nano) + "|" + c.ID
	return base64.URLEncoding.EncodeToString([]byte(raw))
}

// decodePRNamespaceCursor parses a token previously emitted by
// encodePRNamespaceCursor. Empty input returns the zero cursor (no filter).
// Any malformed input wraps ErrInvalidPageToken so the handler can match it
// with errors.Is and surface InvalidArgument (a garbage token is caller
// error, not a server fault — FUT-023 PR #293 review).
func decodePRNamespaceCursor(s string) (prNamespaceCursor, error) {
	if s == "" {
		return prNamespaceCursor{}, nil
	}
	b, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		return prNamespaceCursor{}, fmt.Errorf("%w: decode: %v", ErrInvalidPageToken, err)
	}
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 {
		return prNamespaceCursor{}, fmt.Errorf("%w: malformed", ErrInvalidPageToken)
	}
	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return prNamespaceCursor{}, fmt.Errorf("%w: parse created_at: %v", ErrInvalidPageToken, err)
	}
	return prNamespaceCursor{CreatedAt: ts, ID: parts[1]}, nil
}

// ListPRNamespaces returns a tenant's PR namespaces ordered by
// created_at DESC, id DESC (newest first, id tie-break for stable keyset
// pagination). A non-empty status filters to that lifecycle state
// (callers typically pass "active"); "" lists all states.
//
// pageSize is clamped to [1, 200] with a default of 50 when 0 — matching
// ListScanHistory / ListTenantVulnerabilities. The idx_pr_namespaces_
// tenant_status index backs the (tenant_id, status) predicate. We
// over-fetch one row (LIMIT pageSize+1) to detect the next page boundary
// and, when there is one, emit a keyset cursor as nextPageToken.
func (r *Repository) ListPRNamespaces(ctx context.Context, tenantID uuid.UUID, status string, pageSize int, pageToken string) (namespaces []PRNamespace, nextPageToken string, err error) {
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	cursor, err := decodePRNamespaceCursor(pageToken)
	if err != nil {
		return nil, "", err
	}

	// $1 tenant is always present. The status filter and the keyset
	// condition are appended positionally so an empty status / first page
	// falls through to a straight tenant-wide list.
	q := `SELECT ` + prNamespaceCols + ` FROM pr_namespaces WHERE tenant_id = $1`
	args := []any{tenantID}
	next := 2
	if status != "" {
		q += fmt.Sprintf(" AND status = $%d", next)
		args = append(args, status)
		next++
	}
	if pageToken != "" {
		// Keyset: rows strictly older than the cursor, tie-broken by id.
		q += fmt.Sprintf(
			" AND (created_at < $%d::timestamptz OR (created_at = $%d::timestamptz AND id::text < $%d))",
			next, next, next+1,
		)
		args = append(args, cursor.CreatedAt, cursor.ID)
		next += 2
	}
	q += fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT $%d", next)
	args = append(args, pageSize+1)

	rows, err := r.reader().Query(ctx, q, args...)
	if err != nil {
		return nil, "", fmt.Errorf("list pr namespaces: %w", err)
	}
	defer rows.Close()

	out := make([]PRNamespace, 0, pageSize)
	for rows.Next() {
		ns, err := scanPRNamespace(rows)
		if err != nil {
			return nil, "", fmt.Errorf("scan pr namespace row: %w", err)
		}
		out = append(out, *ns)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list pr namespaces rows: %w", err)
	}

	// Over-fetched one row to detect the next page.
	if len(out) > pageSize {
		last := out[pageSize-1]
		nextPageToken = encodePRNamespaceCursor(prNamespaceCursor{
			CreatedAt: last.CreatedAt,
			ID:        last.ID.String(),
		})
		out = out[:pageSize]
	}
	return out, nextPageToken, nil
}

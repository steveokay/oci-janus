// Package policy owns the per-repo → org → tenant → default inheritance
// chain for scan policies (FE-API-049). Extracted from the gRPC handler
// so both the GetEffectiveScanPolicy RPC and the push.completed
// consumer can resolve a policy without duplicating the chain logic.
//
// The chain semantics:
//
//  1. Per-repo override wins when present AND enabled.
//  2. Org default applies when no per-repo override exists OR the
//     per-repo override is explicitly disabled. The org default ITSELF
//     must be enabled to propagate (mirrors FE-API-039 retention).
//  3. Tenant policy is the bottom-of-chain persisted fallback. The
//     pre-FE-API-049 scan_policies table has no `enabled` column, so
//     tenant rows are treated as always-enabled (backward compatible).
//  4. The synthesised default (auto_scan_on_push=true) is returned
//     when nothing is persisted anywhere — same shape the pre-existing
//     GetScanPolicy cache-miss path returns so callers never have to
//     branch on "no policy".
//
// Resolve never returns ErrNotFound — the synthesised default makes the
// chain total.
package policy

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/scanner/internal/repository"
)

// Source labels the tier the resolved policy came from. Mirrors the
// EffectiveScanPolicy proto field of the same name so we don't lose
// fidelity on the way to the wire.
type Source string

const (
	SourceRepo    Source = "repo"
	SourceOrg     Source = "org"
	SourceTenant  Source = "tenant"
	SourceDefault Source = "default"
)

// Repo is the narrow read surface this resolver needs from
// repository.Repository. Declared here so tests can inject a fake
// without depending on the full repository — same pattern the gc and
// metadata services use.
type Repo interface {
	GetRepoScanPolicy(ctx context.Context, tenantID, repoID uuid.UUID) (*repository.ScanPolicy, error)
	GetOrgScanPolicy(ctx context.Context, tenantID, orgID uuid.UUID) (*repository.ScanPolicy, error)
	GetScanPolicy(ctx context.Context, tenantID uuid.UUID) (*repository.ScanPolicy, error)
}

// Resolved is the resolver's output. Policy is never nil — when no row
// exists anywhere we return a synthesised default policy with Source
// set to "default".
type Resolved struct {
	Policy *repository.ScanPolicy
	Source Source
}

// Resolve walks the chain. repoID == uuid.Nil skips the per-repo tier
// (used by callers that only know about an org). orgID == uuid.Nil and
// no repoID means we go straight to the tenant tier — useful for
// non-repo-scoped admin views.
//
// The orgID parameter is an optional hint: when set, we use it
// directly. When zero AND there is a (possibly-disabled) per-repo row,
// we borrow that row's org_id so we don't need a metadata round-trip.
func Resolve(ctx context.Context, r Repo, tenantID, repoID, orgID uuid.UUID) (*Resolved, error) {
	// Tier 1 — per-repo override.
	if repoID != uuid.Nil {
		rec, err := r.GetRepoScanPolicy(ctx, tenantID, repoID)
		if err == nil {
			if rec.Enabled {
				return &Resolved{Policy: rec, Source: SourceRepo}, nil
			}
			// Disabled override — fall through to org. If the caller
			// didn't supply org_id, borrow it from the disabled row so
			// we can keep resolving without a metadata round-trip.
			if orgID == uuid.Nil && rec.OrgID != uuid.Nil {
				orgID = rec.OrgID
			}
		} else if !errors.Is(err, repository.ErrNotFound) {
			return nil, err
		}
	}

	// Tier 2 — org default. Only enabled defaults propagate.
	if orgID != uuid.Nil {
		rec, err := r.GetOrgScanPolicy(ctx, tenantID, orgID)
		switch {
		case err == nil && rec.Enabled:
			return &Resolved{Policy: rec, Source: SourceOrg}, nil
		case err == nil:
			// Disabled org default — fall through to tenant.
		case errors.Is(err, repository.ErrNotFound):
			// No row — fall through to tenant.
		default:
			return nil, err
		}
	}

	// Tier 3 — tenant policy. No enabled column; treat as always-on.
	rec, err := r.GetScanPolicy(ctx, tenantID)
	if err == nil {
		return &Resolved{Policy: rec, Source: SourceTenant}, nil
	}
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return nil, err
	}

	// Tier 4 — synthesised default. Matches the pre-FE-API-049
	// GetScanPolicy cache-miss behaviour so consumers that already
	// branched on "no row" still see the same shape.
	return &Resolved{
		Policy: &repository.ScanPolicy{
			TenantID:          tenantID,
			AutoScanOnPush:    true,
			BlockOnSeverity:   "",
			ExemptCVEs:        []string{},
			ScannerPlugin:     "trivy",
			ScannerVersionPin: "",
			Enabled:           true,
		},
		Source: SourceDefault,
	}, nil
}

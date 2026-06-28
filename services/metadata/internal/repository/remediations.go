// Package repository — FE-API-017 (remediation suggestions) query.
//
// Mirrors the FE-API-014 (vulnerabilities) pattern: a single CTE picks the
// latest complete scan per (tenant, repo_id, manifest_digest), the JSONB
// `findings` array is parsed in Go, and the results are grouped server-side
// into "upgrade package X from A to B" rows. Tenant isolation is enforced
// in the SQL `WHERE tenant_id = $1` clause and the JOIN's tenant predicates.
//
// JSONB key set we depend on (mirrors libs/scanner/plugin.Finding):
//
//	{ "CVE": "...", "Severity": "...", "Package": "...",
//	  "Version": "...", "FixedIn": "...", "Description": "..." }
//
// "Version" here is the installed version on the image; "FixedIn" is the
// upstream version that fixes the CVE. We group by (Package, Version,
// FixedIn) and skip groups where FixedIn == Version because that's not an
// actionable upgrade.
package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ─── Public row types ───────────────────────────────────────────────────────

// RemediationAffectedRow is one (repo, tag, digest) tuple where a particular
// remediation grouping applies. Mirrors metadatav1.RemediationAffected so the
// handler can map 1:1.
type RemediationAffectedRow struct {
	Repo   string
	Tag    string
	Digest string
}

// RemediationRow is one (package, from, to) grouping rolled up across the
// tenant's latest complete scan per (repo, manifest_digest). `CVEsFixed` is
// the deduplicated CVE list; `MaxSeverity` is the worst severity observed in
// the grouped findings. `Affected` is capped at 10 entries — the true count
// lives in `AffectedCount` so the UI can show "N affected (showing 10)".
type RemediationRow struct {
	PackageName    string
	FromVersion    string
	ToVersion      string
	CVEsFixed      []string
	CVEsFixedCount int32
	MaxSeverity    string
	Affected       []RemediationAffectedRow
	AffectedCount  int32
}

// affectedCap is the maximum number of (repo, tag, digest) tuples returned
// per remediation row. `AffectedCount` reports the true total so callers can
// decide whether to drill into the full list via another endpoint.
const affectedCap = 10

// ─── Cursor ─────────────────────────────────────────────────────────────────

// remediationCursor encodes the full ordering tuple used by the SQL +
// in-memory sort, so a follow-up page resumes exactly where the previous one
// left off. The order is:
//
//	(max_severity_rank ASC, cves_fixed_count DESC, package_name ASC,
//	 from_version ASC, to_version ASC)
//
// Because cves_fixed_count is descending, the cursor stores its NEGATIVE so
// every cursor field can be compared lexicographically with the same "after"
// predicate.
type remediationCursor struct {
	SeverityRank int
	NegCVECount  int // negated cves_fixed_count so DESC becomes ASC
	PackageName  string
	FromVersion  string
	ToVersion    string
}

// encodeRemediationCursor base64-encodes the cursor as
// "<sev>|<negCount>|<pkg>|<from>|<to>". `|` is safe — package/version names
// don't contain pipes in any practical scanner output.
func encodeRemediationCursor(c remediationCursor) string {
	raw := strconv.Itoa(c.SeverityRank) + "|" +
		strconv.Itoa(c.NegCVECount) + "|" +
		c.PackageName + "|" + c.FromVersion + "|" + c.ToVersion
	return base64.URLEncoding.EncodeToString([]byte(raw))
}

// decodeRemediationCursor parses a base64 cursor previously emitted by
// encodeRemediationCursor. Empty input returns the zero cursor (first page).
func decodeRemediationCursor(s string) (remediationCursor, error) {
	if s == "" {
		return remediationCursor{}, nil
	}
	b, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		return remediationCursor{}, fmt.Errorf("decode page_token: %w", err)
	}
	parts := strings.SplitN(string(b), "|", 5)
	if len(parts) != 5 {
		return remediationCursor{}, errors.New("malformed page_token")
	}
	rank, err := strconv.Atoi(parts[0])
	if err != nil {
		return remediationCursor{}, fmt.Errorf("parse rank: %w", err)
	}
	neg, err := strconv.Atoi(parts[1])
	if err != nil {
		return remediationCursor{}, fmt.Errorf("parse neg count: %w", err)
	}
	return remediationCursor{
		SeverityRank: rank,
		NegCVECount:  neg,
		PackageName:  parts[2],
		FromVersion:  parts[3],
		ToVersion:    parts[4],
	}, nil
}

// ─── Sort & rollup helpers ──────────────────────────────────────────────────

// sortRemediationRows orders rows in-place by
// (max_severity_rank ASC, cves_fixed_count DESC, package_name ASC,
//
//	from_version ASC, to_version ASC). Extracted so the test can exercise it
//
// directly without seeding Postgres.
func sortRemediationRows(rows []RemediationRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		ar, br := severityRank(a.MaxSeverity), severityRank(b.MaxSeverity)
		if ar != br {
			return ar < br
		}
		// Higher CVE counts first.
		if a.CVEsFixedCount != b.CVEsFixedCount {
			return a.CVEsFixedCount > b.CVEsFixedCount
		}
		if a.PackageName != b.PackageName {
			return a.PackageName < b.PackageName
		}
		if a.FromVersion != b.FromVersion {
			return a.FromVersion < b.FromVersion
		}
		return a.ToVersion < b.ToVersion
	})
}

// cursorAfter returns true when row sorts strictly after cursor in the same
// ordering as sortRemediationRows. Used to skip rows on a follow-up page.
func cursorAfter(row RemediationRow, c remediationCursor) bool {
	rank := severityRank(row.MaxSeverity)
	if rank != c.SeverityRank {
		return rank > c.SeverityRank
	}
	// Cursor stores -count; row's "rank in sort" is -CVEsFixedCount. A row
	// sorts AFTER cursor when its negated count is greater (smaller in
	// magnitude).
	negCount := -int(row.CVEsFixedCount)
	if negCount != c.NegCVECount {
		return negCount > c.NegCVECount
	}
	if row.PackageName != c.PackageName {
		return row.PackageName > c.PackageName
	}
	if row.FromVersion != c.FromVersion {
		return row.FromVersion > c.FromVersion
	}
	return row.ToVersion > c.ToVersion
}

// rollupRemediations groups (package, version, fixedIn) findings observed
// across scanRows into RemediationRow values. `scanRows` is one row per
// (repo, tag, manifest_digest, findingsJSON) tuple — the same shape produced
// by ListTenantVulnerabilities' inner SQL.
//
// Skips groups where fixedIn is empty or equals the installed version
// (nothing to upgrade). Deduplicates affected tuples per group. Caps each
// group's Affected slice at affectedCap but always reports the true count
// in AffectedCount so the dashboard can render "N affected (showing 10)".
func rollupRemediations(scanRows []remediationScanRow) []RemediationRow {
	type groupKey struct {
		pkg, from, to string
	}
	byGroup := map[groupKey]*RemediationRow{}
	// Per-group sets for dedup of CVE ids and affected tuples — small enough
	// to live in maps without bloating memory for a typical tenant.
	cveSeen := map[groupKey]map[string]struct{}{}
	affectedSeen := map[groupKey]map[RemediationAffectedRow]struct{}{}

	for _, sr := range scanRows {
		for _, f := range sr.Findings {
			if f.FixedIn == "" || f.FixedIn == f.Version {
				continue
			}
			k := groupKey{pkg: f.Package, from: f.Version, to: f.FixedIn}
			row := byGroup[k]
			if row == nil {
				row = &RemediationRow{
					PackageName: f.Package,
					FromVersion: f.Version,
					ToVersion:   f.FixedIn,
					MaxSeverity: strings.ToUpper(f.Severity),
				}
				byGroup[k] = row
				cveSeen[k] = map[string]struct{}{}
				affectedSeen[k] = map[RemediationAffectedRow]struct{}{}
			} else {
				// Update max severity (lower rank == higher severity).
				if severityRank(f.Severity) < severityRank(row.MaxSeverity) {
					row.MaxSeverity = strings.ToUpper(f.Severity)
				}
			}
			// Deduplicate CVE ids — the same CVE can appear in multiple
			// scans for the same package/version pair.
			if f.CVE != "" {
				if _, dup := cveSeen[k][f.CVE]; !dup {
					cveSeen[k][f.CVE] = struct{}{}
					row.CVEsFixed = append(row.CVEsFixed, f.CVE)
				}
			}
			// Deduplicate affected (repo, tag, digest) tuples. Always bump
			// AffectedCount; only append to Affected while under the cap.
			tup := RemediationAffectedRow{Repo: sr.Repo, Tag: sr.Tag, Digest: sr.Digest}
			if _, dup := affectedSeen[k][tup]; !dup {
				affectedSeen[k][tup] = struct{}{}
				row.AffectedCount++
				if int(row.AffectedCount) <= affectedCap {
					row.Affected = append(row.Affected, tup)
				}
			}
		}
	}

	// Finalise CVE count from the deduped slice.
	out := make([]RemediationRow, 0, len(byGroup))
	for _, row := range byGroup {
		// Sort CVE ids for deterministic test output.
		sort.Strings(row.CVEsFixed)
		row.CVEsFixedCount = int32(len(row.CVEsFixed))
		out = append(out, *row)
	}
	return out
}

// remediationScanRow is the in-memory representation of one
// (repo, tag, manifest_digest, findings) row pulled from the latest-scan
// CTE. Exposed only within the package for use by rollupRemediations.
type remediationScanRow struct {
	Repo     string
	Tag      string
	Digest   string
	Findings []scannerFinding
}

// ─── Main query ─────────────────────────────────────────────────────────────

// ListTenantRemediations returns actionable upgrade groupings for the
// tenant. Implementation strategy mirrors ListTenantVulnerabilities:
//
//   - One CTE picks the latest complete scan per (repo_id, manifest_digest).
//   - We LEFT JOIN tags so a manifest with no tag still surfaces (empty tag).
//   - findings JSONB is read once per latest-scan row and decoded in Go.
//     Doing the rollup in SQL is awkward because the affected-tuple cap is
//     per-group and group keys are derived from JSONB fields.
//   - The rollup, sort, cursor skip, and pagination all happen in Go after
//     the rows are fetched. For a tenant with thousands of CVEs across
//     hundreds of tags the SQL result set is still bounded by the tag count
//     (not the finding count), so memory stays modest.
//
// limit is clamped to 1..200; default 50.
func (r *Repository) ListTenantRemediations(
	ctx context.Context,
	tenantID, pageToken string,
	limit int,
) ([]RemediationRow, string, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	cursor, err := decodeRemediationCursor(pageToken)
	if err != nil {
		return nil, "", err
	}

	// Same shape as the FE-API-014 CTE — one row per (repo, tag, digest,
	// findings) — but we don't need the completed_at timestamp here because
	// remediations don't track first/last seen.
	const q = `
		WITH latest AS (
			SELECT DISTINCT ON (sr.repo_id, sr.manifest_digest)
			       sr.repo_id, sr.manifest_digest, sr.findings
			FROM   scan_results sr
			WHERE  sr.tenant_id = $1 AND sr.status = 'complete'
			ORDER  BY sr.repo_id, sr.manifest_digest, sr.completed_at DESC NULLS LAST, sr.created_at DESC
		)
		SELECT (o.name || '/' || r.name) AS repo_name,
		       COALESCE(t.name, '')      AS tag_name,
		       latest.manifest_digest,
		       latest.findings
		FROM   latest
		JOIN   repositories r  ON r.id = latest.repo_id  AND r.tenant_id = $1
		JOIN   organizations o ON o.id = r.org_id
		LEFT   JOIN tags t     ON t.repo_id = latest.repo_id
		                       AND t.manifest_digest = latest.manifest_digest
		                       AND t.tenant_id = $1`

	rows, err := r.reader().Query(ctx, q, tenantID)
	if err != nil {
		return nil, "", fmt.Errorf("ListTenantRemediations query: %w", err)
	}
	defer rows.Close()

	scanRows := make([]remediationScanRow, 0, 64)
	for rows.Next() {
		var sr remediationScanRow
		var findingsJSON []byte
		if err := rows.Scan(&sr.Repo, &sr.Tag, &sr.Digest, &findingsJSON); err != nil {
			return nil, "", fmt.Errorf("scan remediation row: %w", err)
		}
		if len(findingsJSON) == 0 {
			continue
		}
		if err := json.Unmarshal(findingsJSON, &sr.Findings); err != nil {
			// Skip rows with malformed findings rather than failing the
			// whole call — a single bad row shouldn't sink the dashboard.
			continue
		}
		scanRows = append(scanRows, sr)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("ListTenantRemediations rows: %w", err)
	}

	all := rollupRemediations(scanRows)
	sortRemediationRows(all)

	// Apply cursor by skipping rows up to and including the cursor position.
	if pageToken != "" {
		idx := 0
		for i, row := range all {
			if cursorAfter(row, cursor) {
				idx = i
				break
			}
			idx = i + 1
		}
		all = all[idx:]
	}

	// Page slice + next cursor (over-fetch one row to detect boundary).
	var next string
	if len(all) > limit {
		last := all[limit-1]
		next = encodeRemediationCursor(remediationCursor{
			SeverityRank: severityRank(last.MaxSeverity),
			NegCVECount:  -int(last.CVEsFixedCount),
			PackageName:  last.PackageName,
			FromVersion:  last.FromVersion,
			ToVersion:    last.ToVersion,
		})
		all = all[:limit]
	}
	return all, next, nil
}

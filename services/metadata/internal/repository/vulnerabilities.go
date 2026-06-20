// Package repository — FE-API-014 (workspace-wide vulnerability list) and
// FE-API-015 (scan history) queries.
//
// Kept in their own file so they don't bloat the central repository.go.
// All queries are tenant-scoped on every row (CLAUDE.md §9) and use the read
// replica via r.reader() when one is configured (REM-008).
package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ─── FE-API-014: workspace-wide vulnerabilities ─────────────────────────────

// affectedTagRow is one (repo, tag, digest) tuple where a CVE was observed.
type affectedTagRow struct {
	Repo   string
	Tag    string
	Digest string
}

// VulnerabilityRow is one CVE rolled up across every affected tag in the
// tenant's latest scan per (repo, manifest_digest). Mirrors the
// metadatav1.TenantVulnerability proto so the handler can map 1:1.
type VulnerabilityRow struct {
	CVE            string
	Severity       string
	Title          string
	Description    string
	FixedIn        string
	PackageName    string
	PackageVersion string
	Affected       []affectedTagRow
	FirstSeen      time.Time
	LastSeen       time.Time
}

// severityRank assigns a stable sort key to each severity string. CRITICAL
// sorts first (rank 1) so callers see the most urgent findings without an
// extra filter pass. Unknown / lowercased values land in rank 99 to keep
// them stable but pushed to the end.
func severityRank(sev string) int {
	switch strings.ToUpper(sev) {
	case "CRITICAL":
		return 1
	case "HIGH":
		return 2
	case "MEDIUM":
		return 3
	case "LOW":
		return 4
	case "NEGLIGIBLE":
		return 5
	default:
		return 99
	}
}

// vulnerabilityCursor is the base64-encoded (severity_rank|cve_id) keyset
// cursor returned to callers as next_page_token. Pure ASCII; encodes/decodes
// with the URL-safe base64 alphabet so a token can be passed verbatim in a
// URL query string.
type vulnerabilityCursor struct {
	SeverityRank int
	CVEID        string
}

// encodeVulnerabilityCursor base64-encodes (severity_rank|cve_id). The pipe
// separator is safe because cve_id never contains "|".
func encodeVulnerabilityCursor(c vulnerabilityCursor) string {
	raw := strconv.Itoa(c.SeverityRank) + "|" + c.CVEID
	return base64.URLEncoding.EncodeToString([]byte(raw))
}

// decodeVulnerabilityCursor parses a base64 cursor previously emitted by
// encodeVulnerabilityCursor. Returns an error on any malformed input so
// the handler can surface InvalidArgument.
func decodeVulnerabilityCursor(s string) (vulnerabilityCursor, error) {
	if s == "" {
		return vulnerabilityCursor{}, nil
	}
	b, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		return vulnerabilityCursor{}, fmt.Errorf("decode page_token: %w", err)
	}
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 {
		return vulnerabilityCursor{}, errors.New("malformed page_token")
	}
	rank, err := strconv.Atoi(parts[0])
	if err != nil {
		return vulnerabilityCursor{}, fmt.Errorf("parse rank: %w", err)
	}
	return vulnerabilityCursor{SeverityRank: rank, CVEID: parts[1]}, nil
}

// scannerFinding mirrors libs/scanner/plugin.Finding for JSON unmarshaling
// out of scan_results.findings. Capitalised JSON field names match what the
// scanner serialises via json.Marshal on the Go struct.
type scannerFinding struct {
	CVE         string   `json:"CVE"`
	Severity    string   `json:"Severity"`
	Package     string   `json:"Package"`
	Version     string   `json:"Version"`
	FixedIn     string   `json:"FixedIn"`
	Description string   `json:"Description"`
	References  []string `json:"References"`
}

// ListTenantVulnerabilities returns a workspace-wide vulnerability list,
// grouped by cve_id, computed from the latest complete scan per
// (tenant, repo_id, manifest_digest).
//
// Implementation strategy:
//   - One CTE picks the latest complete scan per (repo_id, manifest_digest).
//   - A second SELECT joins that against `tags` so we surface a tag string per
//     affected manifest. We deliberately do this work in Go (not in SQL) for
//     the CVE rollup itself because the findings list is a JSONB array and
//     postgres `jsonb_array_elements` would emit one row per finding per
//     (repo, tag) — bloating result rows for tenants with thousands of CVEs.
//     The JSONB column is read once per latest-scan row and decoded in Go.
//   - severity filter applies BEFORE the rollup so we don't waste memory on
//     findings we'd just drop.
//   - Cursor is base64(severity_rank|cve_id); ascending sort puts CRITICAL
//     first. We over-fetch one row to detect the next page boundary.
//
// limit caps results; the handler is responsible for the 1..200 clamp.
func (r *Repository) ListTenantVulnerabilities(
	ctx context.Context,
	tenantID, severityFilter, pageToken string,
	limit int,
) ([]VulnerabilityRow, string, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	cursor, err := decodeVulnerabilityCursor(pageToken)
	if err != nil {
		return nil, "", err
	}

	// latest scan per (repo_id, manifest_digest) with the tag + org name
	// joined in. Because a manifest may be referenced by multiple tags (or
	// none — pending GC), we emit one row per matching tag and let the
	// rollup in Go deduplicate by cve_id. When no tag references the
	// manifest the LEFT JOIN still emits one row with NULL tag so the CVE
	// is not silently lost; the handler renders that as an empty tag.
	const q = `
		WITH latest AS (
			SELECT DISTINCT ON (sr.repo_id, sr.manifest_digest)
			       sr.repo_id, sr.manifest_digest, sr.findings, sr.completed_at
			FROM   scan_results sr
			WHERE  sr.tenant_id = $1 AND sr.status = 'complete'
			ORDER  BY sr.repo_id, sr.manifest_digest, sr.completed_at DESC NULLS LAST, sr.created_at DESC
		)
		SELECT (o.name || '/' || r.name) AS repo_name,
		       COALESCE(t.name, '')      AS tag_name,
		       latest.manifest_digest,
		       latest.findings,
		       latest.completed_at
		FROM   latest
		JOIN   repositories r  ON r.id = latest.repo_id  AND r.tenant_id = $1
		JOIN   organizations o ON o.id = r.org_id
		LEFT   JOIN tags t     ON t.repo_id = latest.repo_id
		                       AND t.manifest_digest = latest.manifest_digest
		                       AND t.tenant_id = $1`

	rows, err := r.reader().Query(ctx, q, tenantID)
	if err != nil {
		return nil, "", fmt.Errorf("ListTenantVulnerabilities query: %w", err)
	}
	defer rows.Close()

	// Roll up by cve_id. The map is keyed by CVE; insertion-order is preserved
	// in a parallel slice so we can sort deterministically before paginating.
	byCVE := map[string]*VulnerabilityRow{}
	for rows.Next() {
		var repoName, tagName, digest string
		var findingsJSON []byte
		var completedAt *time.Time
		if err := rows.Scan(&repoName, &tagName, &digest, &findingsJSON, &completedAt); err != nil {
			return nil, "", fmt.Errorf("scan vulnerability row: %w", err)
		}
		if len(findingsJSON) == 0 {
			continue
		}
		var findings []scannerFinding
		if err := json.Unmarshal(findingsJSON, &findings); err != nil {
			// Skip rows with malformed findings rather than failing the
			// whole call — a single bad row shouldn't sink the dashboard.
			continue
		}
		var ts time.Time
		if completedAt != nil {
			ts = *completedAt
		}
		for _, f := range findings {
			if f.CVE == "" {
				continue
			}
			if severityFilter != "" && !strings.EqualFold(f.Severity, severityFilter) {
				continue
			}
			v := byCVE[f.CVE]
			if v == nil {
				v = &VulnerabilityRow{
					CVE:            f.CVE,
					Severity:       strings.ToUpper(f.Severity),
					Title:          f.CVE,           // scanner doesn't emit a separate title; fall back to CVE id
					Description:    f.Description,
					FixedIn:        f.FixedIn,
					PackageName:    f.Package,
					PackageVersion: f.Version,
					FirstSeen:      ts,
					LastSeen:       ts,
				}
				byCVE[f.CVE] = v
			}
			// Deduplicate affected entries within this CVE.
			tup := affectedTagRow{Repo: repoName, Tag: tagName, Digest: digest}
			dup := false
			for _, a := range v.Affected {
				if a == tup {
					dup = true
					break
				}
			}
			if !dup {
				v.Affected = append(v.Affected, tup)
			}
			if !ts.IsZero() {
				if ts.Before(v.FirstSeen) || v.FirstSeen.IsZero() {
					v.FirstSeen = ts
				}
				if ts.After(v.LastSeen) {
					v.LastSeen = ts
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("ListTenantVulnerabilities rows: %w", err)
	}

	// Sort by (severity_rank ASC, cve_id ASC) so CRITICAL/HIGH surface first
	// and identical severities tie-break deterministically — a requirement
	// for keyset pagination to be stable across calls.
	out := make([]VulnerabilityRow, 0, len(byCVE))
	for _, v := range byCVE {
		out = append(out, *v)
	}
	sortVulnerabilityRows(out)

	// Apply the keyset cursor by skipping rows <= (cursor.SeverityRank, cursor.CVEID).
	if pageToken != "" {
		idx := 0
		for i, v := range out {
			rank := severityRank(v.Severity)
			if rank > cursor.SeverityRank || (rank == cursor.SeverityRank && v.CVE > cursor.CVEID) {
				idx = i
				break
			}
			idx = i + 1
		}
		out = out[idx:]
	}

	// Page slice + next cursor.
	var next string
	if len(out) > limit {
		last := out[limit-1]
		next = encodeVulnerabilityCursor(vulnerabilityCursor{
			SeverityRank: severityRank(last.Severity),
			CVEID:        last.CVE,
		})
		out = out[:limit]
	}
	return out, next, nil
}

// sortVulnerabilityRows sorts in-place by (severity_rank ASC, cve_id ASC).
// Extracted so the test can exercise it directly.
func sortVulnerabilityRows(rows []VulnerabilityRow) {
	// Simple insertion sort is fine — n is bounded by the tenant's CVE count
	// which for a typical workspace is in the hundreds, not thousands.
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0; j-- {
			aRank, bRank := severityRank(rows[j-1].Severity), severityRank(rows[j].Severity)
			if aRank < bRank || (aRank == bRank && rows[j-1].CVE <= rows[j].CVE) {
				break
			}
			rows[j-1], rows[j] = rows[j], rows[j-1]
		}
	}
}

// ─── FE-API-015: scan history ───────────────────────────────────────────────

// ScanHistoryRow is one scan_results row enriched with repo + tag context.
// Mirrors metadatav1.ScanHistoryEntry so the handler maps 1:1. `Tag` is
// empty when the manifest_digest is no longer tagged.
type ScanHistoryRow struct {
	ScanID         string
	Repo           string // "org/name"
	Tag            string
	ManifestDigest string
	Scanner        string
	StartedAt      time.Time
	CompletedAt    time.Time
	Status         string
	Critical       int32
	High           int32
	Medium         int32
	Low            int32
	Negligible     int32
	Trigger        string
}

// scanHistoryCursor encodes the keyset cursor (completed_at, scan_id) DESC.
// We store completed_at as RFC3339Nano so the cursor is human-readable when
// base64-decoded during debugging; the scan_id is stored verbatim.
type scanHistoryCursor struct {
	CompletedAt time.Time
	ScanID      string
}

// encodeScanHistoryCursor base64-encodes (completed_at|scan_id). A pipe
// separator is safe because RFC3339Nano timestamps cannot contain "|" and
// scan_id is a UUID.
func encodeScanHistoryCursor(c scanHistoryCursor) string {
	raw := c.CompletedAt.Format(time.RFC3339Nano) + "|" + c.ScanID
	return base64.URLEncoding.EncodeToString([]byte(raw))
}

// decodeScanHistoryCursor parses a base64 cursor previously emitted by
// encodeScanHistoryCursor. Empty input returns the zero cursor (no filter).
func decodeScanHistoryCursor(s string) (scanHistoryCursor, error) {
	if s == "" {
		return scanHistoryCursor{}, nil
	}
	b, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		return scanHistoryCursor{}, fmt.Errorf("decode page_token: %w", err)
	}
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 {
		return scanHistoryCursor{}, errors.New("malformed page_token")
	}
	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return scanHistoryCursor{}, fmt.Errorf("parse completed_at: %w", err)
	}
	return scanHistoryCursor{CompletedAt: ts, ScanID: parts[1]}, nil
}

// ListScanHistory returns scan_results rows for tenantID ordered by
// completed_at DESC, scan_id DESC. `since` filters scans whose completed_at
// is greater than or equal to the cutoff. NULL completed_at rows (pending /
// running scans) sort last via NULLS LAST and don't participate in the
// keyset cursor — they appear only on the first page when the keyset hasn't
// started yet.
//
// The composite index `idx_scan_results_tenant_completed_at` on
// (tenant_id, completed_at DESC NULLS LAST, id DESC) backs this query
// efficiently.
func (r *Repository) ListScanHistory(
	ctx context.Context,
	tenantID string,
	since time.Time,
	pageToken string,
	limit int,
) ([]ScanHistoryRow, string, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	cursor, err := decodeScanHistoryCursor(pageToken)
	if err != nil {
		return nil, "", err
	}

	// Build a single keyset condition. Pending rows (completed_at IS NULL)
	// are excluded once we've started paginating to keep ordering stable —
	// users see them on page 1 only.
	args := []any{tenantID, since}
	// LEFT JOIN on tags so a still-tagged manifest surfaces its tag, but
	// a since-retagged scan still appears (with empty tag). DISTINCT ON
	// guarantees one row per scan_results.id even when multiple tags
	// reference the same digest — we deliberately pick any one tag
	// (alphabetical for determinism).
	var keysetCond string
	if pageToken != "" {
		keysetCond = `
			AND (
				sr.completed_at < $3::timestamptz
				OR (sr.completed_at = $3::timestamptz AND sr.id::text < $4)
			)`
		args = append(args, cursor.CompletedAt, cursor.ScanID)
	}

	q := fmt.Sprintf(`
		SELECT DISTINCT ON (sr.completed_at, sr.id)
		       sr.id::text,
		       (o.name || '/' || r.name)            AS repo_name,
		       COALESCE(t.name, '')                 AS tag_name,
		       sr.manifest_digest,
		       sr.scanner_name,
		       sr.started_at,
		       sr.completed_at,
		       sr.status,
		       sr.severity_counts,
		       sr.trigger
		FROM   scan_results sr
		JOIN   repositories  r ON r.id  = sr.repo_id  AND r.tenant_id = $1
		JOIN   organizations o ON o.id  = r.org_id
		LEFT   JOIN tags     t ON t.repo_id = sr.repo_id
		                      AND t.manifest_digest = sr.manifest_digest
		                      AND t.tenant_id = $1
		WHERE  sr.tenant_id = $1
		  AND (sr.completed_at IS NULL OR sr.completed_at >= $2::timestamptz)%s
		ORDER  BY sr.completed_at DESC NULLS LAST, sr.id DESC, t.name ASC
		LIMIT  %d`, keysetCond, limit+1)

	rows, err := r.reader().Query(ctx, q, args...)
	if err != nil {
		return nil, "", fmt.Errorf("ListScanHistory query: %w", err)
	}
	defer rows.Close()

	out := make([]ScanHistoryRow, 0, limit)
	for rows.Next() {
		var row ScanHistoryRow
		var severityJSON []byte
		var startedAt, completedAt *time.Time
		if err := rows.Scan(
			&row.ScanID, &row.Repo, &row.Tag, &row.ManifestDigest, &row.Scanner,
			&startedAt, &completedAt, &row.Status, &severityJSON, &row.Trigger,
		); err != nil {
			return nil, "", fmt.Errorf("scan history row: %w", err)
		}
		if startedAt != nil {
			row.StartedAt = *startedAt
		}
		if completedAt != nil {
			row.CompletedAt = *completedAt
		}
		if len(severityJSON) > 0 {
			var counts map[string]int32
			if err := json.Unmarshal(severityJSON, &counts); err == nil {
				row.Critical = counts["CRITICAL"]
				row.High = counts["HIGH"]
				row.Medium = counts["MEDIUM"]
				row.Low = counts["LOW"]
				row.Negligible = counts["NEGLIGIBLE"]
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("ListScanHistory rows: %w", err)
	}

	// Over-fetched one row to detect the next page.
	var next string
	if len(out) > limit {
		last := out[limit-1]
		next = encodeScanHistoryCursor(scanHistoryCursor{
			CompletedAt: last.CompletedAt,
			ScanID:      last.ScanID,
		})
		out = out[:limit]
	}
	return out, next, nil
}


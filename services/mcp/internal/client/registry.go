// Package client wraps the management BFF HTTP API in a thin typed
// interface. Every tool in services/mcp/internal/tools calls through
// this client — no direct HTTP construction lives in a tool handler.
// Centralising the Bearer-header attachment + non-2xx mapping here
// keeps two load-bearing guarantees testable:
//
//  1. The API key never leaks — see stripKeyFromURL and the deliberate
//     omission of any key field on the error type.
//  2. Every call is a GET — see the Doer interface. Tests substitute a
//     recording Doer to assert the read-only invariant.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Doer is the minimum HTTP surface the client needs. Tests substitute a
// fake to assert method + URL invariants without hitting the network.
// *http.Client satisfies this trivially.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Registry is the thin wrapper. Fields are private so callers can't
// mutate the API key or base URL after construction.
type Registry struct {
	baseURL  string
	apiKey   string
	tenantID string
	doer     Doer
}

// NewRegistry constructs a Registry with a default *http.Client that has
// sensible timeouts. Prefer this in production. Tests use NewRegistryWithDoer.
func NewRegistry(baseURL, apiKey, tenantID string) *Registry {
	// Timeouts guard the tool call — an LLM waiting on a hung BFF
	// request would surface as a session hang. 30s is generous for the
	// list endpoints while still finite.
	c := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second,
		},
	}
	return &Registry{
		baseURL:  strings.TrimRight(baseURL, "/"),
		apiKey:   apiKey,
		tenantID: tenantID,
		doer:     c,
	}
}

// NewRegistryWithDoer is the test constructor. Any Doer works.
func NewRegistryWithDoer(baseURL, apiKey, tenantID string, doer Doer) *Registry {
	return &Registry{
		baseURL:  strings.TrimRight(baseURL, "/"),
		apiKey:   apiKey,
		tenantID: tenantID,
		doer:     doer,
	}
}

// APIError is the typed error surface for non-2xx BFF responses. Body is
// truncated + never contains the request URL (which would echo query
// arguments the LLM supplied — arguably fine, but keeping the error
// surface small avoids surprises).
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("management API returned %d: %s", e.StatusCode, e.Message)
}

// IsNotFound reports whether err is a 404 from the BFF. Used by the
// promotions tool to fall back to a "FUT-020 not deployed" message when
// the endpoint doesn't exist on this registry yet.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// getJSON performs a single GET, attaches the Bearer + tenant headers,
// and unmarshals the response body into out. All read-only tools funnel
// through this method — tests assert `req.Method == http.MethodGet` on
// every recorded request.
func (r *Registry) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	u := r.baseURL + path
	if len(query) > 0 {
		u = u + "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	// Bearer-form API key — MUST NOT appear in slog output.
	req.Header.Set("Authorization", "Bearer "+r.apiKey)
	// X-Tenant-ID pins the tenant the BFF resolves the actor against.
	// The BFF validates that the API key's tenant matches this header;
	// mismatch surfaces as 403.
	req.Header.Set("X-Tenant-ID", r.tenantID)
	req.Header.Set("Accept", "application/json")

	resp, err := r.doer.Do(req)
	if err != nil {
		// Wrap without any header echo — the underlying error MAY
		// contain the URL but never the auth header value.
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	// Cap the body read at 16 MiB — the BFF paginates responses so a
	// single call should be well under this, but guard against a
	// misbehaving upstream flooding the LLM's context window.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 512 {
			msg = msg[:512] + "..."
		}
		return &APIError{StatusCode: resp.StatusCode, Message: msg}
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// -----------------------------------------------------------------------------
// Typed methods — one per BFF surface the MCP tools consume.
//
// Return-shapes intentionally shallow: only the fields the tools surface
// to the LLM. Extra fields on the BFF response are ignored via
// json.Unmarshal's default behavior.
// -----------------------------------------------------------------------------

// Repository is the shape the list_repositories tool surfaces.
type Repository struct {
	Org               string `json:"org"`
	Name              string `json:"name"`
	CreatedAt         string `json:"created_at,omitempty"`
	ImmutableTags     bool   `json:"immutable_tags,omitempty"`
	RequireSignature  bool   `json:"require_signature,omitempty"`
	VisibilityPrivate bool   `json:"visibility_private,omitempty"`
}

// ListRepositories proxies GET /api/v1/repositories?org=<org>.
// org may be empty — the BFF returns all orgs the caller can see.
func (r *Registry) ListRepositories(ctx context.Context, org string) ([]Repository, error) {
	q := url.Values{}
	if org != "" {
		q.Set("org", org)
	}
	var out struct {
		Repositories []Repository `json:"repositories"`
	}
	if err := r.getJSON(ctx, "/api/v1/repositories", q, &out); err != nil {
		return nil, err
	}
	return out.Repositories, nil
}

// Tag is the shape the list_tags tool surfaces.
type Tag struct {
	Name           string `json:"name"`
	ManifestDigest string `json:"manifest_digest"`
	SizeBytes      int64  `json:"size_bytes"`
	LastPulledAt   string `json:"last_pulled_at,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	Immutable      bool   `json:"immutable,omitempty"`
}

// ListTags proxies GET /api/v1/repositories/{org}/{repo}/tags.
func (r *Registry) ListTags(ctx context.Context, org, repo string) ([]Tag, error) {
	path := fmt.Sprintf("/api/v1/repositories/%s/%s/tags", url.PathEscape(org), url.PathEscape(repo))
	var out struct {
		Tags []Tag `json:"tags"`
	}
	if err := r.getJSON(ctx, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Tags, nil
}

// Manifest is the shape the get_manifest tool surfaces.
type Manifest struct {
	MediaType     string          `json:"media_type"`
	SchemaVersion int             `json:"schema_version,omitempty"`
	Digest        string          `json:"digest"`
	SizeBytes     int64           `json:"size_bytes,omitempty"`
	Layers        []ManifestLayer `json:"layers,omitempty"`
	Config        *ManifestBlob   `json:"config,omitempty"`
	Raw           json.RawMessage `json:"raw,omitempty"`
}

// ManifestLayer is a single layer entry inside a manifest.
type ManifestLayer struct {
	MediaType string `json:"media_type"`
	Digest    string `json:"digest"`
	SizeBytes int64  `json:"size_bytes"`
}

// ManifestBlob is the manifest config descriptor.
type ManifestBlob struct {
	MediaType string `json:"media_type"`
	Digest    string `json:"digest"`
	SizeBytes int64  `json:"size_bytes"`
}

// GetManifest proxies GET /api/v1/repositories/{org}/{repo}/manifests/{tag}.
func (r *Registry) GetManifest(ctx context.Context, org, repo, tag string) (*Manifest, error) {
	path := fmt.Sprintf("/api/v1/repositories/%s/%s/manifests/%s",
		url.PathEscape(org), url.PathEscape(repo), url.PathEscape(tag))
	m := &Manifest{}
	if err := r.getJSON(ctx, path, nil, m); err != nil {
		return nil, err
	}
	return m, nil
}

// ServiceAccount is the shape the list_service_accounts tool surfaces.
type ServiceAccount struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	AllowedScopes  []string `json:"allowed_scopes,omitempty"`
	DisabledAt     string   `json:"disabled_at,omitempty"`
	ActiveKeyCount int      `json:"active_key_count,omitempty"`
	CreatedAt      string   `json:"created_at,omitempty"`
}

// ListServiceAccounts proxies GET /api/v1/service-accounts.
func (r *Registry) ListServiceAccounts(ctx context.Context) ([]ServiceAccount, error) {
	var out struct {
		ServiceAccounts []ServiceAccount `json:"service_accounts"`
	}
	if err := r.getJSON(ctx, "/api/v1/service-accounts", nil, &out); err != nil {
		return nil, err
	}
	return out.ServiceAccounts, nil
}

// StaleKey is the shape the list_stale_keys tool surfaces.
type StaleKey struct {
	KeyID           string `json:"key_id"`
	OwnerID         string `json:"owner_id"`
	OwnerKind       string `json:"owner_kind"`
	OwnerLabel      string `json:"owner_label,omitempty"`
	LastUsedAt      string `json:"last_used_at,omitempty"`
	AgeDays         int    `json:"age_days"`
	SuggestedAction string `json:"suggested_action,omitempty"`
}

// ListStaleKeys proxies GET /api/v1/access/review/stale.
func (r *Registry) ListStaleKeys(ctx context.Context) ([]StaleKey, error) {
	var out struct {
		StaleKeys []StaleKey `json:"stale_keys"`
	}
	if err := r.getJSON(ctx, "/api/v1/access/review/stale", nil, &out); err != nil {
		return nil, err
	}
	return out.StaleKeys, nil
}

// AuditFilter drives ListAuditEvents. Any field left zero-valued is
// omitted from the outbound query so the BFF applies its own defaults.
type AuditFilter struct {
	ActionPrefix string
	ActorID      string
	Resource     string
	SinceISO     string
	Limit        int
}

// AuditLimitCap is the maximum number of rows any single call to
// ListAuditEvents may request. Enforced client-side so an LLM cannot
// accidentally exfil the whole audit trail in one go — an important
// invariant for a read-only surface. Any Limit > cap is coerced down.
const AuditLimitCap = 500

// AuditEvent is the shape the list_audit_events tool surfaces.
type AuditEvent struct {
	ID         string `json:"id"`
	OccurredAt string `json:"occurred_at"`
	Action     string `json:"action"`
	ActorID    string `json:"actor_id,omitempty"`
	ActorKind  string `json:"actor_kind,omitempty"`
	Resource   string `json:"resource,omitempty"`
	Outcome    string `json:"outcome,omitempty"`
	IPAddress  string `json:"ip_address,omitempty"`
}

// ListAuditEvents proxies GET /api/v1/audit with filter query params.
// f.Limit is capped at AuditLimitCap regardless of what the LLM passes.
func (r *Registry) ListAuditEvents(ctx context.Context, f AuditFilter) ([]AuditEvent, error) {
	q := url.Values{}
	if f.ActionPrefix != "" {
		q.Set("action_prefix", f.ActionPrefix)
	}
	if f.ActorID != "" {
		q.Set("actor_id", f.ActorID)
	}
	if f.Resource != "" {
		q.Set("resource", f.Resource)
	}
	if f.SinceISO != "" {
		q.Set("since", f.SinceISO)
	}
	limit := f.Limit
	if limit <= 0 || limit > AuditLimitCap {
		limit = AuditLimitCap
	}
	q.Set("limit", fmt.Sprintf("%d", limit))
	var out struct {
		Events []AuditEvent `json:"events"`
	}
	if err := r.getJSON(ctx, "/api/v1/audit", q, &out); err != nil {
		return nil, err
	}
	return out.Events, nil
}

// ScanReport is the shape the get_scan_report tool surfaces. Only the
// summary fields — the LLM doesn't need every CVE row.
type ScanReport struct {
	Digest      string      `json:"digest"`
	ScannedAt   string      `json:"scanned_at,omitempty"`
	ScannerName string      `json:"scanner_name,omitempty"`
	Severities  SeverityMap `json:"severities"`
	TopCVEs     []CVE       `json:"top_cves,omitempty"`
	SBOMPresent bool        `json:"sbom_present,omitempty"`
	ReportURL   string      `json:"report_url,omitempty"`
}

// SeverityMap is the CVE counts by severity.
type SeverityMap struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Info     int `json:"info,omitempty"`
	Unknown  int `json:"unknown,omitempty"`
}

// CVE is a single vulnerability entry.
type CVE struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Package  string `json:"package,omitempty"`
	Version  string `json:"version,omitempty"`
	FixedIn  string `json:"fixed_in,omitempty"`
	Summary  string `json:"summary,omitempty"`
}

// GetScanReport proxies GET /api/v1/repositories/{org}/{repo}/scans/{digest}.
func (r *Registry) GetScanReport(ctx context.Context, org, repo, digest string) (*ScanReport, error) {
	path := fmt.Sprintf("/api/v1/repositories/%s/%s/scans/%s",
		url.PathEscape(org), url.PathEscape(repo), url.PathEscape(digest))
	rep := &ScanReport{}
	if err := r.getJSON(ctx, path, nil, rep); err != nil {
		return nil, err
	}
	return rep, nil
}

// Signature is the shape the list_signatures tool surfaces.
type Signature struct {
	KeyID     string `json:"key_id"`
	Algorithm string `json:"algorithm"`
	SignedAt  string `json:"signed_at,omitempty"`
	Signer    string `json:"signer,omitempty"`
	Backend   string `json:"backend,omitempty"` // cosign / notary
}

// ListSignatures proxies GET /api/v1/repositories/{org}/{repo}/signatures/{digest}.
func (r *Registry) ListSignatures(ctx context.Context, org, repo, digest string) ([]Signature, error) {
	path := fmt.Sprintf("/api/v1/repositories/%s/%s/signatures/%s",
		url.PathEscape(org), url.PathEscape(repo), url.PathEscape(digest))
	var out struct {
		Signatures []Signature `json:"signatures"`
	}
	if err := r.getJSON(ctx, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Signatures, nil
}

// Promotion is the shape the list_promotions tool surfaces. Depends on
// FUT-020 having shipped — if the BFF returns 404, the tool falls back
// to a human-readable "not deployed" message.
type Promotion struct {
	ID         string `json:"id"`
	PromotedAt string `json:"promoted_at"`
	FromOrg    string `json:"from_org"`
	FromRepo   string `json:"from_repo"`
	FromTag    string `json:"from_tag"`
	ToOrg      string `json:"to_org"`
	ToRepo     string `json:"to_repo"`
	ToTag      string `json:"to_tag"`
	Digest     string `json:"digest"`
	ActorID    string `json:"actor_id,omitempty"`
	Note       string `json:"note,omitempty"`
}

// ListPromotions proxies GET /api/v1/repositories/{org}/{repo}/promotions.
// org+repo may both be empty — the BFF returns platform-wide promotions.
func (r *Registry) ListPromotions(ctx context.Context, org, repo string) ([]Promotion, error) {
	var path string
	if org != "" && repo != "" {
		path = fmt.Sprintf("/api/v1/repositories/%s/%s/promotions",
			url.PathEscape(org), url.PathEscape(repo))
	} else {
		path = "/api/v1/promotions"
	}
	var out struct {
		Promotions []Promotion `json:"promotions"`
	}
	if err := r.getJSON(ctx, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Promotions, nil
}

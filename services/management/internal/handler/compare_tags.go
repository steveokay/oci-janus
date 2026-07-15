// Package handler — compare_tags.go
//
// Tier 2 #3 — image diff between two tags.
//
//	GET /api/v1/repositories/{org}/{repo}/compare?from={tagA}&to={tagB}
//
// Computes the delta between two tags of the SAME repository across four
// dimensions, each of which rides entirely on data the BFF can already fetch
// (mirroring the Provenance feature — no new metadata proto/RPC/migration):
//
//   - Layers:          added / removed layer digests + total size delta, from
//     each tag's manifest raw_json (meta.GetManifest).
//   - Config:          ENV / CMD / ENTRYPOINT / exposed-ports / workdir / user
//     deltas, from the OCI image-config blob (core.GetBlob).
//   - Packages:        added / removed / version-changed packages, from each
//     tag's SPDX SBOM (meta.GetScanSBOM).
//   - Vulnerabilities: introduced / fixed CVEs, from each tag's scan findings
//     (meta.GetScanResult).
//
// Every dimension degrades gracefully: a tag with no SBOM, no scan, or a
// deployment without registry-signer/core wired yields an `available:false`
// section with a `reason` rather than failing the whole diff. Only the layer
// diff (always derivable from the manifest) is mandatory.
//
// Auth: repo `reader` — a diff exposes nothing a reader couldn't already see
// by opening each tag individually. Matches handleListPromotions.
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// ─── Response wire shape ────────────────────────────────────────────────────

// compareResponse is the JSON body of GET …/compare. Each section is
// self-describing (availability + reason) so the FE renders per-section empty
// states without inferring from missing fields.
type compareResponse struct {
	From            compareSide `json:"from"`
	To              compareSide `json:"to"`
	Layers          layerDiff   `json:"layers"`
	Config          configDiff  `json:"config"`
	Packages        packageDiff `json:"packages"`
	Vulnerabilities vulnDiff    `json:"vulnerabilities"`
}

// compareSide identifies one end of the comparison.
type compareSide struct {
	Tag       string `json:"tag"`
	Digest    string `json:"digest"`
	SizeBytes int64  `json:"size_bytes"`
	IsIndex   bool   `json:"is_index"`
}

// layerRef is a single layer descriptor in the added/removed lists.
type layerRef struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	MediaType string `json:"media_type,omitempty"`
}

// layerDiff is always present — the manifest is always available.
type layerDiff struct {
	Added          []layerRef `json:"added"`
	Removed        []layerRef `json:"removed"`
	CommonCount    int        `json:"common_count"`
	SizeDeltaBytes int64      `json:"size_delta_bytes"`
}

// envChange records a single environment variable whose value differs.
type envChange struct {
	Key  string `json:"key"`
	From string `json:"from"`
	To   string `json:"to"`
}

// envDiff groups the ENV deltas. Added/Removed carry full "KEY=VALUE" strings;
// Changed carries the key plus both values.
type envDiff struct {
	Added   []string    `json:"added"`
	Removed []string    `json:"removed"`
	Changed []envChange `json:"changed"`
}

// configDiff is the image-config delta. Available is false (with Reason) when
// the config blob can't be fetched (core unwired, an index manifest with no
// single config, or a transport error) — the rest of the diff still returns.
type configDiff struct {
	Available           bool     `json:"available"`
	Reason              string   `json:"reason,omitempty"`
	Env                 envDiff  `json:"env"`
	CmdChanged          bool     `json:"cmd_changed"`
	FromCmd             []string `json:"from_cmd,omitempty"`
	ToCmd               []string `json:"to_cmd,omitempty"`
	EntrypointChanged   bool     `json:"entrypoint_changed"`
	FromEntrypoint      []string `json:"from_entrypoint,omitempty"`
	ToEntrypoint        []string `json:"to_entrypoint,omitempty"`
	ExposedPortsAdded   []string `json:"exposed_ports_added"`
	ExposedPortsRemoved []string `json:"exposed_ports_removed"`
	WorkingDirFrom      string   `json:"working_dir_from,omitempty"`
	WorkingDirTo        string   `json:"working_dir_to,omitempty"`
	UserFrom            string   `json:"user_from,omitempty"`
	UserTo              string   `json:"user_to,omitempty"`
}

// pkgRef is a package name+version in the added/removed lists.
type pkgRef struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// pkgChange records a package present in both tags at a different version.
type pkgChange struct {
	Name        string `json:"name"`
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`
}

// packageDiff is the SBOM-derived delta. Available is false (with Reason) when
// either tag lacks an SBOM (unscanned, or scanned pre-SBOM-support).
type packageDiff struct {
	Available bool        `json:"available"`
	Reason    string      `json:"reason,omitempty"`
	Added     []pkgRef    `json:"added"`
	Removed   []pkgRef    `json:"removed"`
	Changed   []pkgChange `json:"changed"`
}

// vulnRef is a CVE in the introduced/fixed lists.
type vulnRef struct {
	CVE      string `json:"cve"`
	Severity string `json:"severity,omitempty"`
	Package  string `json:"package,omitempty"`
	Version  string `json:"version,omitempty"`
	FixedIn  string `json:"fixed_in,omitempty"`
}

// vulnDiff is the scan-findings delta. Available is false (with Reason) when
// either tag has no scan on record. Added = CVEs present only in `to`
// (introduced); Removed = CVEs present only in `from` (fixed/dropped).
type vulnDiff struct {
	Available bool      `json:"available"`
	Reason    string    `json:"reason,omitempty"`
	Added     []vulnRef `json:"added"`
	Removed   []vulnRef `json:"removed"`
}

// ─── Pure diff helpers (unit-tested directly) ───────────────────────────────

// diffLayers computes the layer delta between two manifests' layer lists. A
// layer is keyed by its digest (content-addressed, so identical content ⇒
// identical digest). Added preserves `to` order; Removed preserves `from`
// order. The size delta is passed in from the manifests' authoritative
// total-size fields rather than summed here, so it stays correct for indexes
// and shared/deduplicated layers.
func diffLayers(from, to []manifestLayer, fromTotal, toTotal int64) layerDiff {
	fromSet := make(map[string]struct{}, len(from))
	for _, l := range from {
		fromSet[l.Digest] = struct{}{}
	}
	toSet := make(map[string]struct{}, len(to))
	for _, l := range to {
		toSet[l.Digest] = struct{}{}
	}

	d := layerDiff{
		Added:          []layerRef{},
		Removed:        []layerRef{},
		SizeDeltaBytes: toTotal - fromTotal,
	}
	for _, l := range to {
		if _, ok := fromSet[l.Digest]; !ok {
			d.Added = append(d.Added, layerRef{Digest: l.Digest, Size: l.Size, MediaType: l.MediaType})
		} else {
			d.CommonCount++
		}
	}
	for _, l := range from {
		if _, ok := toSet[l.Digest]; !ok {
			d.Removed = append(d.Removed, layerRef{Digest: l.Digest, Size: l.Size, MediaType: l.MediaType})
		}
	}
	return d
}

// ociImageConfig is the subset of the OCI/Docker image-config JSON we diff.
// The `config` object uses capitalized keys in both the Docker
// (application/vnd.docker.container.image.v1+json) and OCI
// (application/vnd.oci.image.config.v1+json) schemas.
type ociImageConfig struct {
	Config struct {
		Env          []string            `json:"Env"`
		Cmd          []string            `json:"Cmd"`
		Entrypoint   []string            `json:"Entrypoint"`
		ExposedPorts map[string]struct{} `json:"ExposedPorts"`
		WorkingDir   string              `json:"WorkingDir"`
		User         string              `json:"User"`
	} `json:"config"`
}

// diffConfig parses two image-config blobs and computes the ENV / CMD /
// ENTRYPOINT / exposed-ports / workdir / user deltas. Returns an error only
// when a blob is unparseable — callers turn that into an unavailable section.
func diffConfig(fromCfg, toCfg []byte) (configDiff, error) {
	var a, b ociImageConfig
	if err := json.Unmarshal(fromCfg, &a); err != nil {
		return configDiff{}, fmt.Errorf("parse from config: %w", err)
	}
	if err := json.Unmarshal(toCfg, &b); err != nil {
		return configDiff{}, fmt.Errorf("parse to config: %w", err)
	}

	d := configDiff{Available: true}
	d.Env = diffEnv(a.Config.Env, b.Config.Env)
	d.CmdChanged = !stringsEqual(a.Config.Cmd, b.Config.Cmd)
	if d.CmdChanged {
		d.FromCmd, d.ToCmd = a.Config.Cmd, b.Config.Cmd
	}
	d.EntrypointChanged = !stringsEqual(a.Config.Entrypoint, b.Config.Entrypoint)
	if d.EntrypointChanged {
		d.FromEntrypoint, d.ToEntrypoint = a.Config.Entrypoint, b.Config.Entrypoint
	}
	d.ExposedPortsAdded, d.ExposedPortsRemoved = diffKeySets(a.Config.ExposedPorts, b.Config.ExposedPorts)
	if a.Config.WorkingDir != b.Config.WorkingDir {
		d.WorkingDirFrom, d.WorkingDirTo = a.Config.WorkingDir, b.Config.WorkingDir
	}
	if a.Config.User != b.Config.User {
		d.UserFrom, d.UserTo = a.Config.User, b.Config.User
	}
	return d, nil
}

// diffEnv splits each "KEY=VALUE" entry and reports added / removed keys (as
// full KEY=VALUE strings) plus keys whose value changed.
func diffEnv(from, to []string) envDiff {
	fromMap := envToMap(from)
	toMap := envToMap(to)
	d := envDiff{Added: []string{}, Removed: []string{}, Changed: []envChange{}}

	// Deterministic order: sort the `to` keys for added/changed, `from` for removed.
	toKeys := sortedKeys(toMap)
	for _, k := range toKeys {
		tv := toMap[k]
		fv, ok := fromMap[k]
		if !ok {
			d.Added = append(d.Added, k+"="+tv)
		} else if fv != tv {
			d.Changed = append(d.Changed, envChange{Key: k, From: fv, To: tv})
		}
	}
	for _, k := range sortedKeys(fromMap) {
		if _, ok := toMap[k]; !ok {
			d.Removed = append(d.Removed, k+"="+fromMap[k])
		}
	}
	return d
}

// envToMap splits "KEY=VALUE" env entries into a map. A bare "KEY" (no '=')
// maps to an empty value. Later duplicates win, matching runtime env semantics.
func envToMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, e := range env {
		k, v, found := strings.Cut(e, "=")
		if !found {
			m[e] = ""
			continue
		}
		m[k] = v
	}
	return m
}

// diffKeySets returns the keys added to / removed from a set (map key = member).
func diffKeySets(from, to map[string]struct{}) (added, removed []string) {
	added, removed = []string{}, []string{}
	for k := range to {
		if _, ok := from[k]; !ok {
			added = append(added, k)
		}
	}
	for k := range from {
		if _, ok := to[k]; !ok {
			removed = append(removed, k)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// spdxSBOM is the subset of an SPDX-2.3 JSON document we diff — the package
// list with names + versions. The scanner emits one package per finding
// (see services/scanner/internal/report/report.go).
type spdxSBOM struct {
	Packages []struct {
		Name        string `json:"name"`
		VersionInfo string `json:"versionInfo"`
	} `json:"packages"`
}

// diffPackages parses two SPDX SBOM blobs and computes added / removed /
// version-changed packages, keyed by package name. Returns an error only when
// a blob is unparseable.
func diffPackages(fromSBOM, toSBOM []byte) (packageDiff, error) {
	var a, b spdxSBOM
	if err := json.Unmarshal(fromSBOM, &a); err != nil {
		return packageDiff{}, fmt.Errorf("parse from sbom: %w", err)
	}
	if err := json.Unmarshal(toSBOM, &b); err != nil {
		return packageDiff{}, fmt.Errorf("parse to sbom: %w", err)
	}
	fromMap := make(map[string]string, len(a.Packages))
	for _, p := range a.Packages {
		fromMap[p.Name] = p.VersionInfo
	}
	toMap := make(map[string]string, len(b.Packages))
	for _, p := range b.Packages {
		toMap[p.Name] = p.VersionInfo
	}

	d := packageDiff{Available: true, Added: []pkgRef{}, Removed: []pkgRef{}, Changed: []pkgChange{}}
	for _, name := range sortedKeys(toMap) {
		tv := toMap[name]
		fv, ok := fromMap[name]
		if !ok {
			d.Added = append(d.Added, pkgRef{Name: name, Version: tv})
		} else if fv != tv {
			d.Changed = append(d.Changed, pkgChange{Name: name, FromVersion: fv, ToVersion: tv})
		}
	}
	for _, name := range sortedKeys(fromMap) {
		if _, ok := toMap[name]; !ok {
			d.Removed = append(d.Removed, pkgRef{Name: name, Version: fromMap[name]})
		}
	}
	return d, nil
}

// scanFinding is the subset of a stored scan finding we diff. findings_json is
// json.Marshal of []plugin.Finding, which has NO json tags, so the keys are
// the capitalized Go field names.
type scanFinding struct {
	CVE      string `json:"CVE"`
	Severity string `json:"Severity"`
	Package  string `json:"Package"`
	Version  string `json:"Version"`
	FixedIn  string `json:"FixedIn"`
}

// diffVulns parses two findings_json blobs and computes introduced (Added) /
// fixed (Removed) CVEs, keyed by CVE id. Unparseable input yields an
// unavailable diff rather than an error — a malformed scan blob shouldn't sink
// the whole comparison.
func diffVulns(fromFindings, toFindings []byte) vulnDiff {
	var a, b []scanFinding
	if err := json.Unmarshal(fromFindings, &a); err != nil {
		return vulnDiff{Available: false, Reason: "unreadable scan findings for the source tag"}
	}
	if err := json.Unmarshal(toFindings, &b); err != nil {
		return vulnDiff{Available: false, Reason: "unreadable scan findings for the target tag"}
	}
	fromMap := make(map[string]scanFinding, len(a))
	for _, f := range a {
		fromMap[f.CVE] = f
	}
	toMap := make(map[string]scanFinding, len(b))
	for _, f := range b {
		toMap[f.CVE] = f
	}

	d := vulnDiff{Available: true, Added: []vulnRef{}, Removed: []vulnRef{}}
	for _, cve := range sortedFindingKeys(toMap) {
		if _, ok := fromMap[cve]; !ok {
			f := toMap[cve]
			d.Added = append(d.Added, vulnRef{CVE: f.CVE, Severity: f.Severity, Package: f.Package, Version: f.Version, FixedIn: f.FixedIn})
		}
	}
	for _, cve := range sortedFindingKeys(fromMap) {
		if _, ok := toMap[cve]; !ok {
			f := fromMap[cve]
			d.Removed = append(d.Removed, vulnRef{CVE: f.CVE, Severity: f.Severity, Package: f.Package, Version: f.Version, FixedIn: f.FixedIn})
		}
	}
	return d
}

// ─── small shared helpers ───────────────────────────────────────────────────

func stringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedFindingKeys(m map[string]scanFinding) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ─── Handler ────────────────────────────────────────────────────────────────

// indexMediaTypes are the manifest media types that denote a multi-arch index
// rather than a single image manifest (which carries no single image config).
var indexMediaTypes = map[string]struct{}{
	"application/vnd.oci.image.index.v1+json":                   {},
	"application/vnd.docker.distribution.manifest.list.v2+json": {},
}

// parsedManifest is the slice of a manifest the diff needs: its layer list,
// config blob digest, total size, and whether it is a multi-arch index.
type parsedManifest struct {
	layers       []manifestLayer
	configDigest string
	sizeBytes    int64
	isIndex      bool
}

// resolveManifestForDiff resolves a tag → its manifest and extracts the slice
// the diff needs. Returns a 404-worthy error when the tag/manifest is missing.
func (h *Handler) resolveManifestForDiff(ctx context.Context, repoID, tenantID, tag string) (parsedManifest, string, error) {
	t, err := h.meta.GetTag(ctx, &metadatav1.GetTagRequest{RepoId: repoID, TenantId: tenantID, Name: tag})
	if err != nil {
		return parsedManifest{}, "", fmt.Errorf("tag %q not found: %w", tag, err)
	}
	digest := t.GetManifestDigest()
	m, err := h.meta.GetManifest(ctx, &metadatav1.GetManifestRequest{RepoId: repoID, TenantId: tenantID, Reference: digest})
	if err != nil {
		return parsedManifest{}, "", fmt.Errorf("manifest for tag %q not found: %w", tag, err)
	}

	pm := parsedManifest{sizeBytes: m.GetSizeBytes()}
	if _, ok := indexMediaTypes[m.GetMediaType()]; ok {
		pm.isIndex = true
	}
	var raw rawManifest
	if err := json.Unmarshal(m.GetRawJson(), &raw); err == nil {
		pm.configDigest = raw.Config.Digest
		for _, l := range raw.Layers {
			pm.layers = append(pm.layers, manifestLayer{Digest: l.Digest, Size: l.Size, MediaType: l.MediaType})
		}
		// An index carries child manifests rather than layers; surface that so
		// the layer diff still shows something meaningful (child digests).
		if len(raw.Manifests) > 0 {
			pm.isIndex = true
			for _, sub := range raw.Manifests {
				pm.layers = append(pm.layers, manifestLayer{Digest: sub.Digest, Size: sub.Size, MediaType: sub.MediaType})
			}
		}
	}
	return pm, digest, nil
}

// handleCompareTags computes the diff between two tags of the same repo.
func (h *Handler) handleCompareTags(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	org, repoName := r.PathValue("org"), r.PathValue("repo")
	fromTag := r.URL.Query().Get("from")
	toTag := r.URL.Query().Get("to")

	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}
	if err := validateRepoName(repoName); err != nil {
		writeError(w, http.StatusBadRequest, "invalid repository name")
		return
	}
	if err := validateTagName(fromTag); err != nil {
		writeError(w, http.StatusBadRequest, "invalid 'from' tag")
		return
	}
	if err := validateTagName(toTag); err != nil {
		writeError(w, http.StatusBadRequest, "invalid 'to' tag")
		return
	}

	// Reader on the repo suffices — a diff exposes nothing a reader couldn't
	// already see by opening each tag individually.
	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "reader") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	fromPM, fromDigest, err := h.resolveManifestForDiff(r.Context(), repo.GetRepoId(), tenantID, fromTag)
	if err != nil {
		writeError(w, http.StatusNotFound, "source tag not found")
		return
	}
	toPM, toDigest, err := h.resolveManifestForDiff(r.Context(), repo.GetRepoId(), tenantID, toTag)
	if err != nil {
		writeError(w, http.StatusNotFound, "target tag not found")
		return
	}

	resp := compareResponse{
		From:   compareSide{Tag: fromTag, Digest: fromDigest, SizeBytes: fromPM.sizeBytes, IsIndex: fromPM.isIndex},
		To:     compareSide{Tag: toTag, Digest: toDigest, SizeBytes: toPM.sizeBytes, IsIndex: toPM.isIndex},
		Layers: diffLayers(fromPM.layers, toPM.layers, fromPM.sizeBytes, toPM.sizeBytes),
	}
	resp.Config = h.buildConfigDiff(r.Context(), tenantID, fromPM, toPM)
	resp.Packages = h.buildPackageDiff(r.Context(), tenantID, fromDigest, toDigest)
	resp.Vulnerabilities = h.buildVulnDiff(r.Context(), tenantID, fromDigest, toDigest)

	writeJSON(w, http.StatusOK, resp)
}

// buildConfigDiff fetches both image-config blobs via registry-core and diffs
// them. Degrades to an unavailable section (with a reason) when core is not
// wired, when either side is an index (no single config), or when a blob can't
// be fetched or parsed — the promotion/layer/package/vuln sections still show.
func (h *Handler) buildConfigDiff(ctx context.Context, tenantID string, from, to parsedManifest) configDiff {
	if h.core == nil {
		return configDiff{Reason: "config diff requires registry-core, which is not wired in this deployment"}
	}
	if from.isIndex || to.isIndex {
		return configDiff{Reason: "config diff is not available for multi-arch index manifests"}
	}
	if from.configDigest == "" || to.configDigest == "" {
		return configDiff{Reason: "one or both manifests have no image config"}
	}
	fromCfg, err := h.fetchBlob(ctx, tenantID, from.configDigest, configBlobCap)
	if err != nil {
		slog.Warn("compare: fetch from config blob", "err", err, "digest", from.configDigest)
		return configDiff{Reason: "could not read the source image config"}
	}
	toCfg, err := h.fetchBlob(ctx, tenantID, to.configDigest, configBlobCap)
	if err != nil {
		slog.Warn("compare: fetch to config blob", "err", err, "digest", to.configDigest)
		return configDiff{Reason: "could not read the target image config"}
	}
	d, err := diffConfig(fromCfg, toCfg)
	if err != nil {
		slog.Warn("compare: parse config blobs", "err", err)
		return configDiff{Reason: "image config was not parseable"}
	}
	return d
}

// buildPackageDiff fetches both SBOMs via metadata and diffs their package
// lists. Degrades to unavailable when either tag has no SBOM on record.
func (h *Handler) buildPackageDiff(ctx context.Context, tenantID, fromDigest, toDigest string) packageDiff {
	fromSBOM, ok := h.fetchSBOM(ctx, tenantID, fromDigest)
	if !ok {
		return packageDiff{Reason: "the source tag has no SBOM — run a scan to generate one"}
	}
	toSBOM, ok := h.fetchSBOM(ctx, tenantID, toDigest)
	if !ok {
		return packageDiff{Reason: "the target tag has no SBOM — run a scan to generate one"}
	}
	d, err := diffPackages(fromSBOM, toSBOM)
	if err != nil {
		slog.Warn("compare: parse sboms", "err", err)
		return packageDiff{Reason: "one or both SBOMs were not parseable"}
	}
	return d
}

// fetchSBOM returns the SBOM bytes for a digest, or ok=false when there is no
// SBOM (NotFound) or a transport error (logged) — the caller degrades either
// way rather than failing the whole diff.
func (h *Handler) fetchSBOM(ctx context.Context, tenantID, digest string) ([]byte, bool) {
	resp, err := h.meta.GetScanSBOM(ctx, &metadatav1.GetScanSBOMRequest{TenantId: tenantID, ManifestDigest: digest})
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			return nil, false
		}
		slog.Warn("compare: GetScanSBOM", "err", err, "digest", digest)
		return nil, false
	}
	return resp.GetSbomJson(), true
}

// buildVulnDiff fetches both scans via metadata and diffs their findings.
// Degrades to unavailable when either tag has no scan on record.
func (h *Handler) buildVulnDiff(ctx context.Context, tenantID, fromDigest, toDigest string) vulnDiff {
	fromFindings, ok := h.fetchFindings(ctx, tenantID, fromDigest)
	if !ok {
		return vulnDiff{Reason: "the source tag has not been scanned"}
	}
	toFindings, ok := h.fetchFindings(ctx, tenantID, toDigest)
	if !ok {
		return vulnDiff{Reason: "the target tag has not been scanned"}
	}
	return diffVulns(fromFindings, toFindings)
}

// fetchFindings returns the findings_json for a digest, or ok=false when there
// is no scan (NotFound) or a transport error. An empty (but present) scan
// yields ok=true with an empty-array blob so the diff reports "no new CVEs".
func (h *Handler) fetchFindings(ctx context.Context, tenantID, digest string) ([]byte, bool) {
	// RepoId is not part of the digest-keyed lookup contract used elsewhere
	// (digest_keyed.go passes only tenant+digest); GetScanResult keys on
	// (tenant_id, manifest_digest).
	resp, err := h.meta.GetScanResult(ctx, &metadatav1.GetScanResultRequest{TenantId: tenantID, ManifestDigest: digest})
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			return nil, false
		}
		slog.Warn("compare: GetScanResult", "err", err, "digest", digest)
		return nil, false
	}
	findings := resp.GetFindingsJson()
	if len(findings) == 0 {
		// A completed scan with zero findings — represent as an empty array so
		// diffVulns treats it as "scanned, no CVEs" rather than unavailable.
		findings = []byte("[]")
	}
	return findings, true
}

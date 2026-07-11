// Package handler — provenance.go
//
// Image lineage / provenance surface (Tier 2 #4).
//
// Surfaces the well-known OCI `org.opencontainers.image.*` predefined
// annotations (git commit, source repo, build URL, vendor/version/created/
// licenses, base image, …) on the tag-detail page's Provenance tab. The
// manifest JSON is already fetched + unmarshalled by handleGetManifest in
// handler.go, so this file only defines the response shape (provenanceInfo)
// and the buildProvenance mapper that lifts the annotation map into it.
//
// This rides on existing plumbing — no new migration, proto, gRPC call, or
// BFF route. handleGetManifest calls buildProvenance(raw.Annotations) and
// stashes the result on ManifestResponse.Provenance.
//
// SECURITY: annotations are attacker-controlled — any pusher can set any
// `org.opencontainers.image.*` label on a manifest. Two guards apply:
//
//  1. URL-bearing fields (url, documentation, source) run through
//     safeExternalURL (chartparse.go) so a `javascript:`/`data:` value can
//     never reach the FE as an anchor href (React does not strip those).
//  2. The payload is bounded — every annotation value is truncated to
//     maxAnnotationValueLen bytes and the raw annotations map is capped at
//     maxRawAnnotations entries so a malicious manifest can't bloat the
//     response.
//
// Lives in its own file so concurrent edits to handler.go don't conflict
// with the provenance feature surface (same convention as referrers.go).
package handler

const (
	// maxAnnotationValueLen caps the byte length of any single annotation
	// value we echo back. Annotations are attacker-controlled, so a pusher
	// could set a multi-megabyte value; we truncate to keep the JSON body
	// bounded. 1 KiB is generous for a git URL / commit / license string.
	maxAnnotationValueLen = 1024

	// maxRawAnnotations caps how many entries land in the raw annotations map
	// so a manifest crammed with thousands of bespoke labels can't bloat the
	// response. The well-known fields are always mapped regardless of this
	// cap; this bound only limits the raw passthrough view.
	maxRawAnnotations = 64
)

// Well-known OCI predefined annotation keys (OCI image-spec annotations).
// Centralised as constants so the mapping in buildProvenance reads cleanly
// and there's a single place to audit the key spellings.
const (
	annCreated       = "org.opencontainers.image.created"
	annAuthors       = "org.opencontainers.image.authors"
	annURL           = "org.opencontainers.image.url"
	annDocumentation = "org.opencontainers.image.documentation"
	annSource        = "org.opencontainers.image.source"
	annVersion       = "org.opencontainers.image.version"
	annRevision      = "org.opencontainers.image.revision"
	annVendor        = "org.opencontainers.image.vendor"
	annLicenses      = "org.opencontainers.image.licenses"
	annRefName       = "org.opencontainers.image.ref.name"
	annTitle         = "org.opencontainers.image.title"
	annDescription   = "org.opencontainers.image.description"
	annBaseName      = "org.opencontainers.image.base.name"
	annBaseDigest    = "org.opencontainers.image.base.digest"
)

// provenanceInfo is the JSON block surfaced on the tag-detail Provenance tab.
//
// Each well-known OCI annotation maps to a snake_case field; all are
// omitempty so a manifest that only sets a couple of labels serialises a
// compact object. `Annotations` carries a bounded raw view of ALL annotations
// (well-known and bespoke alike) so non-standard build-tool metadata stays
// visible in the collapsible raw table on the FE.
//
// The URL-bearing fields (URL, Documentation, Source) have already passed
// through safeExternalURL by the time they land here — see buildProvenance.
type provenanceInfo struct {
	Created       string `json:"created,omitempty"`       // org.opencontainers.image.created (RFC 3339)
	Authors       string `json:"authors,omitempty"`       // org.opencontainers.image.authors
	URL           string `json:"url,omitempty"`           // org.opencontainers.image.url (sanitised)
	Documentation string `json:"documentation,omitempty"` // org.opencontainers.image.documentation (sanitised)
	Source        string `json:"source,omitempty"`        // org.opencontainers.image.source — git repo (sanitised)
	Version       string `json:"version,omitempty"`       // org.opencontainers.image.version
	Revision      string `json:"revision,omitempty"`      // org.opencontainers.image.revision — git commit
	Vendor        string `json:"vendor,omitempty"`        // org.opencontainers.image.vendor
	Licenses      string `json:"licenses,omitempty"`      // org.opencontainers.image.licenses (SPDX expr)
	RefName       string `json:"ref_name,omitempty"`      // org.opencontainers.image.ref.name
	Title         string `json:"title,omitempty"`         // org.opencontainers.image.title
	Description   string `json:"description,omitempty"`   // org.opencontainers.image.description
	BaseName      string `json:"base_name,omitempty"`     // org.opencontainers.image.base.name
	BaseDigest    string `json:"base_digest,omitempty"`   // org.opencontainers.image.base.digest

	// Annotations is a bounded raw view of ALL annotations on the manifest —
	// including non-standard keys (e.g. build-tool metadata) so nothing is
	// hidden from the operator. Capped at maxRawAnnotations entries with each
	// value truncated to maxAnnotationValueLen bytes.
	Annotations map[string]string `json:"annotations,omitempty"`
}

// truncateValue bounds a single annotation value to maxAnnotationValueLen
// bytes. Annotations are attacker-controlled, so an over-long value is cut to
// keep the response payload bounded. Truncation is on a byte boundary — the
// value is opaque display text, not something we re-parse, so a split rune is
// harmless (the FE renders it verbatim).
func truncateValue(v string) string {
	if len(v) > maxAnnotationValueLen {
		return v[:maxAnnotationValueLen]
	}
	return v
}

// buildProvenance lifts the top-level OCI annotation map into a provenanceInfo.
//
// It maps each well-known `org.opencontainers.image.*` key to its typed field,
// sanitises the three URL-bearing fields through safeExternalURL (so a
// `javascript:`/`data:` value becomes ""), truncates every value to
// maxAnnotationValueLen, and carries a bounded raw view of ALL annotations
// (capped at maxRawAnnotations entries) for the collapsible raw table.
//
// Returns nil when the manifest has NO annotations at all — omitempty on the
// ManifestResponse field then drops the whole block and the FE shows its
// empty state (the common case: most tags carry no annotations).
func buildProvenance(annotations map[string]string) *provenanceInfo {
	// No annotations → nil so the whole provenance block is omitted and the
	// FE renders its empty state. This is the common path for most images.
	if len(annotations) == 0 {
		return nil
	}

	p := &provenanceInfo{
		// Non-URL fields are mapped directly, truncated to the value cap.
		Created:     truncateValue(annotations[annCreated]),
		Authors:     truncateValue(annotations[annAuthors]),
		Version:     truncateValue(annotations[annVersion]),
		Revision:    truncateValue(annotations[annRevision]),
		Vendor:      truncateValue(annotations[annVendor]),
		Licenses:    truncateValue(annotations[annLicenses]),
		RefName:     truncateValue(annotations[annRefName]),
		Title:       truncateValue(annotations[annTitle]),
		Description: truncateValue(annotations[annDescription]),
		BaseName:    truncateValue(annotations[annBaseName]),
		BaseDigest:  truncateValue(annotations[annBaseDigest]),
		// URL-bearing fields are attacker-controlled hrefs — sanitise through
		// safeExternalURL (http(s)-only) BEFORE truncating so a dangerous
		// scheme is dropped to "" rather than surviving as a truncated prefix.
		URL:           truncateValue(safeExternalURL(annotations[annURL])),
		Documentation: truncateValue(safeExternalURL(annotations[annDocumentation])),
		Source:        truncateValue(safeExternalURL(annotations[annSource])),
	}

	// Bounded raw passthrough of ALL annotations so bespoke keys stay visible.
	// Cap at maxRawAnnotations entries; each value truncated to the value cap.
	// Map iteration order is unspecified, so which entries survive the cap is
	// non-deterministic — acceptable for a raw debug view, and the well-known
	// fields above are always present regardless.
	raw := make(map[string]string, len(annotations))
	for k, v := range annotations {
		if len(raw) >= maxRawAnnotations {
			break
		}
		raw[k] = truncateValue(v)
	}
	if len(raw) > 0 {
		p.Annotations = raw
	}

	return p
}

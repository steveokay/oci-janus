package repository

import (
	"strings"
	"testing"
)

// S-MAINT-1 Batch 5 (P6 + F4) — pure-function tests for the artifact-type
// helpers. These pin two contracts:
//
//   1. parseConfigMediaType pulls `config.mediaType` out of a typical OCI
//      manifest doc and degrades to "" on parse failure / missing fields.
//   2. deriveArtifactType maps known mediaTypes to stable discriminators
//      and falls through to "other" for unknown-but-present types, "" for
//      the empty-input case.
//
// Each test row is a self-contained scenario — adding a new artifact
// kind is one row + one switch case in deriveArtifactType.

func TestParseConfigMediaType(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "docker image manifest",
			raw:  `{"config":{"mediaType":"application/vnd.docker.container.image.v1+json"}}`,
			want: "application/vnd.docker.container.image.v1+json",
		},
		{
			name: "oci image manifest",
			raw:  `{"schemaVersion":2,"config":{"mediaType":"application/vnd.oci.image.config.v1+json","size":1234}}`,
			want: "application/vnd.oci.image.config.v1+json",
		},
		{
			name: "helm chart manifest",
			raw:  `{"config":{"mediaType":"application/vnd.cncf.helm.config.v1+json"}}`,
			want: "application/vnd.cncf.helm.config.v1+json",
		},
		{
			name: "no config block",
			raw:  `{"schemaVersion":2,"layers":[]}`,
			want: "",
		},
		{
			name: "empty input",
			raw:  ``,
			want: "",
		},
		{
			name: "malformed json",
			raw:  `{not json`,
			want: "",
		},
		{
			name: "config present, mediaType missing",
			raw:  `{"config":{"size":1234}}`,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseConfigMediaType([]byte(tc.raw))
			if got != tc.want {
				t.Errorf("parseConfigMediaType(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestDeriveArtifactType(t *testing.T) {
	cases := []struct {
		name             string
		configMediaType  string
		mediaType        string
		want             string
	}{
		// Container images — both docker + oci variants land on "image".
		{"docker image config", "application/vnd.docker.container.image.v1+json", "", "image"},
		{"oci image config", "application/vnd.oci.image.config.v1+json", "", "image"},
		// Helm.
		{"helm chart", "application/vnd.cncf.helm.config.v1+json", "", "helm"},
		// Cosign signature shapes.
		{"cosign simplesigning", "application/vnd.dev.cosign.simplesigning.v1+json", "", "signature"},
		{"dsse envelope", "application/vnd.dsse.envelope.v1+json", "", "signature"},
		// SBOMs.
		{"spdx sbom", "application/spdx+json", "", "sbom"},
		{"cyclonedx sbom", "application/vnd.cyclonedx+json", "", "sbom"},
		// Unknown but present mediaType → "other". The manifest-level
		// fallback is NOT consulted in this case — once we see a recognised
		// config_media_type taxonomy we trust the answer.
		{"unknown config", "application/vnd.example.unknown.v1+json", "", "other"},
		// REM-020 Fix A: manifest-index / manifest-list rows have NULL
		// config_media_type (an index is a pointer at per-arch manifests,
		// not an image config). Must classify as "image" via the
		// manifest-level mediaType fallback so the repo Tags tab's
		// `?type=image` filter includes multi-arch images.
		{"oci image index", "", "application/vnd.oci.image.index.v1+json", "image"},
		{"docker manifest list", "", "application/vnd.docker.distribution.manifest.list.v2+json", "image"},
		// Both empty → genuine legacy/unknown row.
		{"both empty", "", "", ""},
		// config empty + unrecognised manifest mediaType → unknown.
		{"unknown manifest mediaType", "", "application/vnd.example.unknown.v1+json", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveArtifactType(tc.configMediaType, tc.mediaType)
			if got != tc.want {
				t.Errorf("deriveArtifactType(config=%q, media=%q) = %q, want %q",
					tc.configMediaType, tc.mediaType, got, tc.want)
			}
		})
	}
}

// REM-021 — parseChildManifestDigests must extract the per-platform child
// digests from an OCI image index / Docker manifest list, and return nil
// for any other media type. The retention + size paths depend on the
// non-index return being nil so they don't trip the manifest_children
// upsert branch for single-arch images.
func TestParseChildManifestDigests(t *testing.T) {
	// Realistic OCI image index with two platforms — mirrors what
	// services/core writes through PutManifest for a multi-arch push.
	ociIndex := `{
	  "schemaVersion": 2,
	  "mediaType": "application/vnd.oci.image.index.v1+json",
	  "manifests": [
	    {"digest": "sha256:aaaa00000000000000000000000000000000000000000000000000000000aaaa", "size": 400, "platform": {"architecture": "amd64", "os": "linux"}},
	    {"digest": "sha256:bbbb00000000000000000000000000000000000000000000000000000000bbbb", "size": 410, "platform": {"architecture": "arm64", "os": "linux"}}
	  ]
	}`
	// Docker manifest list — same shape, different mediaType.
	dockerList := `{
	  "schemaVersion": 2,
	  "mediaType": "application/vnd.docker.distribution.manifest.list.v2+json",
	  "manifests": [
	    {"digest": "sha256:cccc00000000000000000000000000000000000000000000000000000000cccc", "size": 420}
	  ]
	}`

	cases := []struct {
		name      string
		raw       string
		mediaType string
		want      []string
	}{
		{
			name:      "oci image index — two children parsed in order",
			raw:       ociIndex,
			mediaType: "application/vnd.oci.image.index.v1+json",
			want: []string{
				"sha256:aaaa00000000000000000000000000000000000000000000000000000000aaaa",
				"sha256:bbbb00000000000000000000000000000000000000000000000000000000bbbb",
			},
		},
		{
			name:      "docker manifest list — single child parsed",
			raw:       dockerList,
			mediaType: "application/vnd.docker.distribution.manifest.list.v2+json",
			want: []string{
				"sha256:cccc00000000000000000000000000000000000000000000000000000000cccc",
			},
		},
		{
			name:      "non-index mediaType returns nil even with manifests[] present",
			raw:       ociIndex, // body has manifests[] but mediaType says single image
			mediaType: "application/vnd.oci.image.manifest.v1+json",
			want:      nil,
		},
		{
			name:      "empty json with index mediaType",
			raw:       ``,
			mediaType: "application/vnd.oci.image.index.v1+json",
			want:      nil,
		},
		{
			name:      "malformed json with index mediaType",
			raw:       `{not json`,
			mediaType: "application/vnd.oci.image.index.v1+json",
			want:      nil,
		},
		{
			name:      "index with empty manifests[]",
			raw:       `{"manifests": []}`,
			mediaType: "application/vnd.oci.image.index.v1+json",
			want:      []string{},
		},
		{
			name:      "duplicate child digests deduped",
			raw:       `{"manifests":[{"digest":"sha256:dddd00000000000000000000000000000000000000000000000000000000dddd"},{"digest":"sha256:dddd00000000000000000000000000000000000000000000000000000000dddd"}]}`,
			mediaType: "application/vnd.oci.image.index.v1+json",
			want: []string{
				"sha256:dddd00000000000000000000000000000000000000000000000000000000dddd",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseChildManifestDigests([]byte(tc.raw), tc.mediaType)
			if len(got) != len(tc.want) {
				t.Fatalf("len: got %d (%v), want %d (%v)", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d]: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// REM-021 — bounded loop guard. A crafted index with thousands of
// `manifests[]` entries must clip to maxManifestEntries so a single
// PutManifest call can't drive an unbounded INSERT against
// manifest_children.
func TestParseChildManifestDigests_BoundedAtMaxEntries(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"manifests":[`)
	const overflow = maxManifestEntries + 50
	for i := 0; i < overflow; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		// Each child has a unique digest so the dedupe path doesn't hide
		// the entry count we're checking.
		b.WriteString(`{"digest":"sha256:` + padHex(i) + `"}`)
	}
	b.WriteString(`]}`)
	got := parseChildManifestDigests([]byte(b.String()), "application/vnd.oci.image.index.v1+json")
	if len(got) != maxManifestEntries {
		t.Errorf("len(got) = %d, want %d (clipped to maxManifestEntries)", len(got), maxManifestEntries)
	}
}

// padHex returns a 64-char hex string seeded by i so test inputs have
// distinct digests without hand-typing each.
func padHex(i int) string {
	const hex = "0123456789abcdef"
	// 64 chars total; encode i across the trailing 8 chars; pad with '0'.
	out := make([]byte, 64)
	for j := range out {
		out[j] = '0'
	}
	for j := 0; j < 8; j++ {
		out[63-j] = hex[(i>>(j*4))&0xf]
	}
	return string(out)
}

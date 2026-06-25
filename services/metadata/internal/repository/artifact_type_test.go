package repository

import (
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

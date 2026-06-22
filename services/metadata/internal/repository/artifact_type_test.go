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
		mediaType string
		want      string
	}{
		// Container images — both docker + oci variants land on "image".
		{"application/vnd.docker.container.image.v1+json", "image"},
		{"application/vnd.oci.image.config.v1+json", "image"},
		// Helm.
		{"application/vnd.cncf.helm.config.v1+json", "helm"},
		// Cosign signature shapes.
		{"application/vnd.dev.cosign.simplesigning.v1+json", "signature"},
		{"application/vnd.dsse.envelope.v1+json", "signature"},
		// SBOMs.
		{"application/spdx+json", "sbom"},
		{"application/vnd.cyclonedx+json", "sbom"},
		// Unknown but present mediaType → "other".
		{"application/vnd.example.unknown.v1+json", "other"},
		// Empty input passes through empty — distinguishes legacy/null
		// rows from "recognised manifest, unknown artifact category".
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.mediaType, func(t *testing.T) {
			got := deriveArtifactType(tc.mediaType)
			if got != tc.want {
				t.Errorf("deriveArtifactType(%q) = %q, want %q", tc.mediaType, got, tc.want)
			}
		})
	}
}

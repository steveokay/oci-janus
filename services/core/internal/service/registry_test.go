// Package service_test contains unit tests for registry-core service helpers.
// Tests here have no network or database dependencies.
package service

import (
	"testing"
)

// TestValidateName_tabledriven verifies the repository name regex against a
// comprehensive set of valid and invalid names (OCI §4.3 / CLAUDE.md §7).
func TestValidateName_tabledriven(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		// Valid names — org/repo format.
		{name: "simple org/repo", input: "myorg/myrepo", wantErr: false},
		{name: "numeric characters", input: "org123/repo456", wantErr: false},
		{name: "hyphen in both parts", input: "my-org/my-repo", wantErr: false},
		{name: "dot separator", input: "my.org/my.repo", wantErr: false},
		{name: "underscore separator", input: "my_org/my_repo", wantErr: false},
		{name: "all lowercase", input: "a/b", wantErr: false},
		{name: "numbers only after separator", input: "org1/repo-2.3_4", wantErr: false},
		// Invalid names.
		{name: "no slash — single component", input: "noslash", wantErr: true},
		{name: "uppercase letters", input: "MyOrg/MyRepo", wantErr: true},
		{name: "leading hyphen in org", input: "-org/repo", wantErr: true},
		{name: "leading hyphen in repo", input: "org/-repo", wantErr: true},
		{name: "trailing hyphen in org", input: "org-/repo", wantErr: true},
		{name: "double slash", input: "org//repo", wantErr: true},
		{name: "empty string", input: "", wantErr: true},
		{name: "spaces", input: "my org/my repo", wantErr: true},
		{name: "three components", input: "org/sub/repo", wantErr: true}, // regex only allows org/repo format
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateName(tc.input)
			if tc.wantErr && err == nil {
				t.Errorf("ValidateName(%q): expected error, got nil", tc.input)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidateName(%q): unexpected error: %v", tc.input, err)
			}
		})
	}
}

// TestValidateDigest_tabledriven verifies the digest regex against valid sha256
// digests and various malformed values.
func TestValidateDigest_tabledriven(t *testing.T) {
	valid := "sha256:" + "a" + "1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2"

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:    "valid sha256 digest",
			input:   valid,
			wantErr: false,
		},
		{
			name:    "all zeros",
			input:   "sha256:" + "0000000000000000000000000000000000000000000000000000000000000000",
			wantErr: false,
		},
		{
			name:    "uppercase hex — rejected",
			input:   "sha256:" + "A000000000000000000000000000000000000000000000000000000000000000",
			wantErr: true,
		},
		{
			name:    "wrong algorithm prefix",
			input:   "sha512:" + "a000000000000000000000000000000000000000000000000000000000000000",
			wantErr: true,
		},
		{
			name:    "too short — 63 hex chars",
			input:   "sha256:" + "a00000000000000000000000000000000000000000000000000000000000000",
			wantErr: true,
		},
		{
			name:    "too long — 65 hex chars",
			input:   "sha256:" + "a0000000000000000000000000000000000000000000000000000000000000000",
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "no colon separator",
			input:   "sha256" + "a000000000000000000000000000000000000000000000000000000000000000",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateDigest(tc.input)
			if tc.wantErr && err == nil {
				t.Errorf("ValidateDigest(%q): expected error, got nil", tc.input)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidateDigest(%q): unexpected error: %v", tc.input, err)
			}
		})
	}
}

// TestBlobKey_format verifies the storage key layout for blobs matches the
// spec in CLAUDE.md §4.4: blobs/<tenant_id>/sha256/<first2>/<full_hex>.
func TestBlobKey_format(t *testing.T) {
	tenantID := "tenant-abc"
	digest := "sha256:aabbccddee0011223344556677889900aabbccddee0011223344556677889900"
	got := blobKey(tenantID, digest)

	// The first two hex chars of the digest determine the shard directory.
	const want = "blobs/tenant-abc/sha256/aa/aabbccddee0011223344556677889900aabbccddee0011223344556677889900"
	if got != want {
		t.Errorf("blobKey: got %q, want %q", got, want)
	}
}

// TestIsGRPCNotFound_notFoundCode verifies that a status.NotFound error is
// recognised by the helper.
func TestIsGRPCNotFound_notFoundCode(t *testing.T) {
	grpcErr := newNotFoundErr()
	if !isGRPCNotFound(grpcErr) {
		t.Error("expected isGRPCNotFound to return true for NotFound status")
	}
}

// TestIsGRPCNotFound_otherCode verifies that a non-NotFound gRPC error returns false.
func TestIsGRPCNotFound_otherCode(t *testing.T) {
	grpcErr := newInternalErr()
	if isGRPCNotFound(grpcErr) {
		t.Error("expected isGRPCNotFound to return false for Internal status")
	}
}

// TestIsGRPCNotFound_nilError confirms that nil is not treated as NotFound.
func TestIsGRPCNotFound_nilError(t *testing.T) {
	if isGRPCNotFound(nil) {
		t.Error("expected isGRPCNotFound to return false for nil error")
	}
}

// TestHasAction_permittedAction verifies a claim grants the expected action.
func TestHasAction_permittedAction(t *testing.T) {
	claims := buildClaims("myorg/myrepo", []string{"push", "pull"})
	if !claims.HasAction("myorg/myrepo", "push") {
		t.Error("expected HasAction to return true for push on the granted repo")
	}
	if !claims.HasAction("myorg/myrepo", "pull") {
		t.Error("expected HasAction to return true for pull on the granted repo")
	}
}

// TestHasAction_deniedAction verifies a claim denies an action not in the list.
func TestHasAction_deniedAction(t *testing.T) {
	claims := buildClaims("myorg/myrepo", []string{"pull"})
	if claims.HasAction("myorg/myrepo", "push") {
		t.Error("expected HasAction to return false for push when only pull is granted")
	}
}

// TestHasAction_wrongRepo verifies a claim does not bleed onto a different repo.
func TestHasAction_wrongRepo(t *testing.T) {
	claims := buildClaims("myorg/myrepo", []string{"push", "pull"})
	if claims.HasAction("myorg/otherrepo", "push") {
		t.Error("expected HasAction to return false for a different repository")
	}
}

// TestHasAction_wildcardName verifies that a wildcard name grants access to any repo.
func TestHasAction_wildcardName(t *testing.T) {
	claims := buildClaims("*", []string{"pull"})
	if !claims.HasAction("anyorg/anyrepo", "pull") {
		t.Error("expected HasAction to return true when name is wildcard")
	}
}

// TestHasAction_wildcardAction verifies that a wildcard action grants anything.
func TestHasAction_wildcardAction(t *testing.T) {
	claims := buildClaims("myorg/myrepo", []string{"*"})
	if !claims.HasAction("myorg/myrepo", "delete") {
		t.Error("expected HasAction to return true when action is wildcard")
	}
}

// TestHasAction_noClaims verifies that empty access list denies everything.
func TestHasAction_noClaims(t *testing.T) {
	claims := &TokenClaims{}
	if claims.HasAction("myorg/myrepo", "push") {
		t.Error("expected HasAction to return false with no access claims")
	}
}

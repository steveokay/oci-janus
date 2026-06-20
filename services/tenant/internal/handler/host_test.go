// Tests for FE-API-007 host-selection algorithm. These exercise
// GRPCHandler.buildTenantProto directly so we don't need a real database —
// the function is pure given a TenantRecord + slice of DomainRecord.
package handler

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/tenant/internal/repository"
)

// TestBuildTenantProto_PrimaryVerified_UsesPrimaryHost covers the most common
// production case: a tenant has at least one verified domain marked primary.
// The host should equal that domain and host_is_custom should be true.
func TestBuildTenantProto_PrimaryVerified_UsesPrimaryHost(t *testing.T) {
	h := New(nil, "registry.example.com")
	rec := &repository.TenantRecord{ID: uuid.New(), Name: "Acme", Slug: "acme", CreatedAt: time.Now()}
	domains := []repository.DomainRecord{
		{Domain: "registry.acme.com", Verified: true, IsPrimary: true},
	}

	got := h.buildTenantProto(rec, domains)

	if got.GetHost() != "registry.acme.com" {
		t.Errorf("host: got %q, want registry.acme.com", got.GetHost())
	}
	if !got.GetHostIsCustom() {
		t.Errorf("host_is_custom: got false, want true")
	}
	if got.GetSlug() != "acme" {
		t.Errorf("slug: got %q, want acme", got.GetSlug())
	}
	if len(got.GetDomains()) != 1 {
		t.Fatalf("domains: got %d entries, want 1", len(got.GetDomains()))
	}
	d := got.GetDomains()[0]
	if d.GetDomain() != "registry.acme.com" || !d.GetVerified() || !d.GetIsPrimary() {
		t.Errorf("domain entry: got %+v, want verified+primary registry.acme.com", d)
	}
}

// TestBuildTenantProto_NoVerifiedPrimary_UsesWildcard exercises the fallback
// path: no primary verified domain exists, so host = `<slug>.<base>` and
// host_is_custom = false. Mirrors a brand-new tenant with no custom domain.
func TestBuildTenantProto_NoVerifiedPrimary_UsesWildcard(t *testing.T) {
	h := New(nil, "registry.example.com")
	rec := &repository.TenantRecord{ID: uuid.New(), Name: "Acme", Slug: "acme", CreatedAt: time.Now()}

	got := h.buildTenantProto(rec, nil)

	if got.GetHost() != "acme.registry.example.com" {
		t.Errorf("host: got %q, want acme.registry.example.com", got.GetHost())
	}
	if got.GetHostIsCustom() {
		t.Errorf("host_is_custom: got true, want false")
	}
}

// TestBuildTenantProto_VerifiedNotPrimary_UsesWildcard covers a defensive
// edge case: every domain is verified but none has is_primary=true (manual
// SQL fix, mid-migration state, etc.). The host should still fall back to
// the wildcard rather than picking a random verified domain.
func TestBuildTenantProto_VerifiedNotPrimary_UsesWildcard(t *testing.T) {
	h := New(nil, "registry.example.com")
	rec := &repository.TenantRecord{ID: uuid.New(), Name: "Acme", Slug: "acme", CreatedAt: time.Now()}
	domains := []repository.DomainRecord{
		{Domain: "registry.acme.com", Verified: true, IsPrimary: false},
		{Domain: "ghcr.acme.com", Verified: true, IsPrimary: false},
	}

	got := h.buildTenantProto(rec, domains)

	if got.GetHost() != "acme.registry.example.com" {
		t.Errorf("host: got %q, want wildcard fallback", got.GetHost())
	}
	if got.GetHostIsCustom() {
		t.Errorf("host_is_custom: got true, want false")
	}
	if len(got.GetDomains()) != 2 {
		t.Errorf("domains: got %d entries, want 2", len(got.GetDomains()))
	}
}

// TestBuildTenantProto_MultiplePrimaryCandidates_PicksFirst covers what
// happens when more than one verified+primary row sneaks through (the
// partial unique index should prevent this — defence in depth). The
// algorithm must be deterministic: take the first one in the input order.
func TestBuildTenantProto_MultiplePrimaryCandidates_PicksFirst(t *testing.T) {
	h := New(nil, "registry.example.com")
	rec := &repository.TenantRecord{ID: uuid.New(), Name: "Acme", Slug: "acme", CreatedAt: time.Now()}
	domains := []repository.DomainRecord{
		{Domain: "first.acme.com", Verified: true, IsPrimary: true},
		{Domain: "second.acme.com", Verified: true, IsPrimary: true},
	}

	got := h.buildTenantProto(rec, domains)

	if got.GetHost() != "first.acme.com" {
		t.Errorf("host: got %q, want first.acme.com (deterministic pick)", got.GetHost())
	}
	if !got.GetHostIsCustom() {
		t.Errorf("host_is_custom: got false, want true")
	}
}

// TestBuildTenantProto_UnverifiedPrimary_FallsBack handles a row that's
// flagged primary but not verified — e.g., a primary domain whose DNS TXT
// challenge failed. We must not advertise an unverified hostname to clients.
func TestBuildTenantProto_UnverifiedPrimary_FallsBack(t *testing.T) {
	h := New(nil, "registry.example.com")
	rec := &repository.TenantRecord{ID: uuid.New(), Name: "Acme", Slug: "acme", CreatedAt: time.Now()}
	domains := []repository.DomainRecord{
		{Domain: "broken.acme.com", Verified: false, IsPrimary: true},
	}

	got := h.buildTenantProto(rec, domains)

	if got.GetHost() != "acme.registry.example.com" {
		t.Errorf("host: got %q, want wildcard (unverified primary must not win)", got.GetHost())
	}
	if got.GetHostIsCustom() {
		t.Errorf("host_is_custom: got true, want false")
	}
}

// TestBuildTenantProto_EmptySlug_UsesTenantID guards the post-backfill edge
// case where slug somehow ends up empty (data corruption, race during
// migration). The host must still be a parseable hostname — fall back to
// the tenant id so the wildcard subdomain is unique.
func TestBuildTenantProto_EmptySlug_UsesTenantID(t *testing.T) {
	h := New(nil, "registry.example.com")
	tid := uuid.New()
	rec := &repository.TenantRecord{ID: tid, Name: "??", Slug: "", CreatedAt: time.Now()}

	got := h.buildTenantProto(rec, nil)

	want := tid.String() + ".registry.example.com"
	if got.GetHost() != want {
		t.Errorf("host: got %q, want %q", got.GetHost(), want)
	}
}

// TestBuildTenantProto_EmptyBaseDomain_UsesBareSlug covers tests / misconfig
// where PLATFORM_BASE_DOMAIN is empty. The host should be the bare slug so
// callers see a hostname rather than the meaningless ".".
func TestBuildTenantProto_EmptyBaseDomain_UsesBareSlug(t *testing.T) {
	h := New(nil, "")
	rec := &repository.TenantRecord{ID: uuid.New(), Name: "Acme", Slug: "acme", CreatedAt: time.Now()}

	got := h.buildTenantProto(rec, nil)

	if got.GetHost() != "acme" {
		t.Errorf("host: got %q, want bare slug 'acme'", got.GetHost())
	}
}

// TestNormalizeSlug_TableDriven covers the slug-normalization algorithm
// shared between the SQL backfill and CreateTenant. The cases capture every
// behaviour the migration depends on so a future change to either path
// triggers a test failure rather than silent drift.
func TestNormalizeSlug_TableDriven(t *testing.T) {
	cases := map[string]string{
		"Acme":                "acme",
		"Acme Corp":           "acme-corp",
		"Acme  Corp":          "acme-corp",   // collapse multi-space
		"acme--corp":          "acme-corp",   // collapse multi-dash
		"  Acme  ":            "acme",        // trim leading/trailing
		"Acme/Corp_Inc":       "acme-corp-inc",
		"":                    "",            // empty → empty (caller falls back to id)
		"!@#$":                "",            // no alphanumerics → empty
		"AlreadySlug123":      "alreadyslug123",
		"-leading-dash":       "leading-dash",
		"trailing-dash-":      "trailing-dash",
	}
	for in, want := range cases {
		got := repository.NormalizeSlug(in)
		if got != want {
			t.Errorf("NormalizeSlug(%q): got %q, want %q", in, got, want)
		}
	}
}

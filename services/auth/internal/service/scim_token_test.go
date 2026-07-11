package service

import (
	"testing"
)

// fakeSCIMRepo is an in-memory scimRepo for the token generate/verify unit test.
type fakeSCIMRepo struct {
	cfg     *fakeCfg
	touched bool
}
type fakeCfg struct {
	tokenHash string
	enabled   bool
}

func (f *fakeSCIMRepo) getHash() (string, bool)   { return f.cfg.tokenHash, f.cfg.enabled }
func (f *fakeSCIMRepo) setHash(h string, en bool) { f.cfg = &fakeCfg{tokenHash: h, enabled: en} }
func (f *fakeSCIMRepo) touch()                    { f.touched = true }

func TestSCIMToken_generate_thenVerify(t *testing.T) {
	svc := newSCIMTokenSvc(&fakeSCIMRepo{cfg: &fakeCfg{}})
	raw, err := svc.generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(raw) < 20 || raw[:5] != "scim." {
		t.Fatalf("raw token should carry the scim. prefix, got %q", raw)
	}
	ok, err := svc.verify(raw)
	if err != nil || !ok {
		t.Fatalf("verify of freshly-generated token must pass, got ok=%v err=%v", ok, err)
	}
	if ok, _ := svc.verify("scim.deadbeef"); ok {
		t.Fatal("verify of a wrong token must fail")
	}
	// A disabled config must never verify, even with the right token.
	svc.repo.(*fakeSCIMRepo).cfg.enabled = false
	if ok, _ := svc.verify(raw); ok {
		t.Fatal("verify must fail when the config is disabled")
	}
}

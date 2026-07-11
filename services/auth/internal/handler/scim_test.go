package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// newSCIMAuthTestHandler wires requireSCIMAuth(verify, next) onto a mux under
// GET /scim/v2/Users, where next writes 200. Lives in the handler package so it
// can reach the unexported requireSCIMAuth.
func newSCIMAuthTestHandler(_ *testing.T, verify scimVerifier) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /scim/v2/Users", requireSCIMAuth(verify, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	return mux
}

func TestRequireSCIMAuth_rejectsBadToken(t *testing.T) {
	verify := func(raw string) (bool, error) { return raw == "scim.good", nil }
	h := newSCIMAuthTestHandler(t, verify)

	// No/blank Authorization → 401.
	req := httptest.NewRequest(http.MethodGet, "/scim/v2/Users", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: want 401, got %d", rec.Code)
	}

	// Wrong token → 401.
	req = httptest.NewRequest(http.MethodGet, "/scim/v2/Users", nil)
	req.Header.Set("Authorization", "Bearer scim.wrong")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: want 401, got %d", rec.Code)
	}

	// Right token → reaches next (200).
	req = httptest.NewRequest(http.MethodGet, "/scim/v2/Users", nil)
	req.Header.Set("Authorization", "Bearer scim.good")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("good token: want 200, got %d", rec.Code)
	}
}

// newSCIMDiscoveryTestHandler registers the discovery routes under
// requireSCIMAuth with an always-true verifier. Discovery handlers are methods
// on *HTTPHandler but read no service state, so a zero-value handler suffices.
func newSCIMDiscoveryTestHandler(_ *testing.T) http.Handler {
	mux := http.NewServeMux()
	h := &HTTPHandler{}
	verify := func(string) (bool, error) { return true, nil }
	g := func(fn http.HandlerFunc) http.HandlerFunc { return requireSCIMAuth(verify, fn) }
	mux.HandleFunc("GET /scim/v2/ServiceProviderConfig", g(h.scimServiceProviderConfig))
	mux.HandleFunc("GET /scim/v2/ResourceTypes", g(h.scimResourceTypes))
	mux.HandleFunc("GET /scim/v2/Schemas", g(h.scimSchemas))
	return mux
}

func TestSCIMDiscovery_serviceProviderConfig(t *testing.T) {
	h := newSCIMDiscoveryTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/scim/v2/ServiceProviderConfig", nil)
	req.Header.Set("Authorization", "Bearer scim.good")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["patch"]; !ok {
		t.Errorf("ServiceProviderConfig must advertise a patch capability")
	}
	if _, ok := body["authenticationSchemes"]; !ok {
		t.Errorf("ServiceProviderConfig must advertise authenticationSchemes")
	}
}

func boolPtr(b bool) *bool { return &b }

func TestParseUserFilter(t *testing.T) {
	cases := []struct {
		in                string
		wantUser, wantExt string
		wantActive        *bool
		wantErr           bool
	}{
		{in: ``, wantUser: "", wantExt: ""},
		{in: `userName eq "alice"`, wantUser: "alice"},
		{in: `externalId eq "ext-1"`, wantExt: "ext-1"},
		{in: `active eq true`, wantActive: boolPtr(true)},
		{in: `active eq false`, wantActive: boolPtr(false)},
		{in: `displayName co "x"`, wantErr: true}, // unsupported
	}
	for _, c := range cases {
		u, e, a, err := parseUserFilter(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: want error", c.in)
			}
			continue
		}
		if err != nil || u != c.wantUser || e != c.wantExt {
			t.Errorf("%q: got user=%q ext=%q err=%v", c.in, u, e, err)
		}
		if (a == nil) != (c.wantActive == nil) || (a != nil && *a != *c.wantActive) {
			t.Errorf("%q: active mismatch", c.in)
		}
	}
}

func TestToSCIMUser_mapsCoreFields(t *testing.T) {
	id := uuid.New()
	dn := "Alice Example"
	u := &repository.User{
		ID:          id,
		Username:    "alice",
		Email:       "alice@example.io",
		DisplayName: &dn,
		IsActive:    true,
	}
	su := toSCIMUser(u, "ext-1")
	if su.ID != id.String() {
		t.Errorf("id: got %q want %q", su.ID, id.String())
	}
	if su.UserName != "alice" || su.ExternalID != "ext-1" || su.DisplayName != dn {
		t.Errorf("core fields mismatch: %+v", su)
	}
	if !su.Active {
		t.Error("active should be true")
	}
	if su.primaryEmail() != "alice@example.io" {
		t.Errorf("primaryEmail: got %q", su.primaryEmail())
	}
	if su.Meta == nil || su.Meta.Location != "/scim/v2/Users/"+id.String() {
		t.Errorf("meta.location mismatch: %+v", su.Meta)
	}
}

// newSCIMUsersTestHandler builds a real Service (via buildTestService's fakes)
// with the SCIM bootstrap tenant set, mounts the /scim/v2 routes behind an
// always-true verifier, and returns the mux + the shared test context so tests
// can assert against the fake user store.
func newSCIMUsersTestHandler(t *testing.T) (http.Handler, *testCtx, uuid.UUID) {
	t.Helper()
	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)
	tenantID := uuid.New()
	// Wire the SCIM bootstrap tenant (the fake user repo also satisfies
	// scimConfigRepo). Verification is bypassed by the always-true verifier.
	tc.svc.SetSCIMRepo(tc.users, tenantID)

	h := NewHTTPHandler(tc.svc, uuid.Nil)
	mux := http.NewServeMux()
	h.RegisterSCIM(mux, func(string) (bool, error) { return true, nil })
	return mux, tc, tenantID
}

func scimReq(method, target, body string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	r.Header.Set("Authorization", "Bearer scim.good")
	r.Header.Set("Content-Type", "application/scim+json")
	return r
}

func TestSCIMUsers_lifecycle(t *testing.T) {
	h, tc, _ := newSCIMUsersTestHandler(t)

	// 1. POST creates a passwordless user → 201.
	body := `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"alice","externalId":"ext1","emails":[{"value":"a@x.io","primary":true}],"active":true}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, scimReq(http.MethodPost, "/scim/v2/Users", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST: want 201, got %d: %s", rec.Code, rec.Body)
	}
	var created scimUser
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.ID == "" || created.ExternalID != "ext1" || created.UserName != "alice" {
		t.Fatalf("created resource mismatch: %+v", created)
	}
	if !created.Active {
		t.Error("created user should be active")
	}

	// 2. GET list filtered by userName → 1 result.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, scimReq(http.MethodGet, `/scim/v2/Users?filter=`+urlEnc(`userName eq "alice"`), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET list: want 200, got %d: %s", rec.Code, rec.Body)
	}
	var list scimListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if list.TotalResults != 1 || len(list.Resources) != 1 {
		t.Fatalf("list by userName: want 1 result, got total=%d len=%d", list.TotalResults, len(list.Resources))
	}

	// GET list filtered by externalId → 1 result.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, scimReq(http.MethodGet, `/scim/v2/Users?filter=`+urlEnc(`externalId eq "ext1"`), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET list by externalId: want 200, got %d", rec.Code)
	}

	// 3. PATCH active:false → 200 and user disabled.
	patch := `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"active","value":false}]}`
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, scimReq(http.MethodPatch, "/scim/v2/Users/"+created.ID, patch))
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH active:false: want 200, got %d: %s", rec.Code, rec.Body)
	}
	if u := tc.users.users["alice"]; u == nil || u.IsActive {
		t.Error("PATCH active:false must disable the user")
	}

	// 4. Unsupported filter → 400 invalidFilter.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, scimReq(http.MethodGet, `/scim/v2/Users?filter=`+urlEnc(`displayName co "x"`), ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unsupported filter: want 400, got %d", rec.Code)
	}
}

func TestSCIMUsers_localPasswordCollision_409(t *testing.T) {
	h, tc, tenantID := newSCIMUsersTestHandler(t)
	// Seed a local-password user with the target email.
	tc.users.users["local"] = &repository.User{
		ID:           uuid.New(),
		TenantID:     tenantID,
		Username:     "local",
		Email:        "dup@x.io",
		PasswordHash: "$argon2id$fake",
		IsActive:     true,
		Kind:         "human",
	}
	body := `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"dup2","externalId":"ext-dup","emails":[{"value":"dup@x.io","primary":true}],"active":true}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, scimReq(http.MethodPost, "/scim/v2/Users", body))
	if rec.Code != http.StatusConflict {
		t.Fatalf("local-password collision: want 409, got %d: %s", rec.Code, rec.Body)
	}
}

// urlEnc is a tiny query-escaper so the filter tests stay readable.
func urlEnc(s string) string {
	return strings.NewReplacer(" ", "%20", `"`, "%22").Replace(s)
}

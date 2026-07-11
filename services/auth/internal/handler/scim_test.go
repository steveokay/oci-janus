package handler

import (
	"context"
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
	verify := func(_ context.Context, raw string) (bool, error) { return raw == "scim.good", nil }
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
	verify := func(context.Context, string) (bool, error) { return true, nil }
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
	if su.Active == nil || !*su.Active {
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
	h.RegisterSCIM(mux, func(context.Context, string) (bool, error) { return true, nil })
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
	if created.Active == nil || !*created.Active {
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

// scimBadCreate is the helper shared by the input-validation cases: it POSTs the
// body and asserts a 400 with scimType=invalidValue (the SCIM error envelope).
func scimBadCreate(t *testing.T, h http.Handler, body string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, scimReq(http.MethodPost, "/scim/v2/Users", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body)
	}
	var e scimError
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if e.SCIMType != "invalidValue" {
		t.Errorf("scimType: want invalidValue, got %q", e.SCIMType)
	}
}

// Fix 1 — POST /scim/v2/Users must reject IdP attributes that fail the §7
// allowlists (bad userName, bad email, over-long externalId) with 400
// invalidValue BEFORE any user is provisioned.
func TestSCIMCreate_rejectsInvalidAttributes(t *testing.T) {
	h, tc, _ := newSCIMUsersTestHandler(t)

	// Bad userName — contains a space (fails ^[a-zA-Z0-9_-]{3,64}$).
	scimBadCreate(t, h, `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"bad name","emails":[{"value":"ok@x.io","primary":true}]}`)
	// Too-long userName (> 64 chars).
	scimBadCreate(t, h, `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"`+strings.Repeat("a", 65)+`","emails":[{"value":"ok2@x.io","primary":true}]}`)
	// Bad email — no domain.
	scimBadCreate(t, h, `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"gooduser","emails":[{"value":"not-an-email","primary":true}]}`)
	// Over-long externalId (> 255 bytes).
	scimBadCreate(t, h, `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"gooduser2","externalId":"`+strings.Repeat("x", 256)+`","emails":[{"value":"ok3@x.io","primary":true}]}`)
	// Over-long displayName (> 255 bytes).
	scimBadCreate(t, h, `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"gooduser3","displayName":"`+strings.Repeat("d", 256)+`","emails":[{"value":"ok4@x.io","primary":true}]}`)

	// None of the rejected requests should have created a user.
	if len(tc.users.users) != 0 {
		t.Fatalf("no invalid create should provision a user, got %d users", len(tc.users.users))
	}

	// Sanity: a fully-valid create still succeeds (guards against over-strict rules).
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, scimReq(http.MethodPost, "/scim/v2/Users",
		`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"valid_user","externalId":"ext-ok","emails":[{"value":"good@x.io","primary":true}]}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("valid create: want 201, got %d: %s", rec.Code, rec.Body)
	}
}

// Fix 2 — requireSCIMAuth must pass the request context (not context.Background)
// into the verifier so the token-verify DB reads are request-scoped.
func TestRequireSCIMAuth_threadsRequestContext(t *testing.T) {
	type ctxKey string
	const k ctxKey = "probe"

	var got any
	verify := func(ctx context.Context, _ string) (bool, error) {
		got = ctx.Value(k)
		return true, nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /scim/v2/Users", requireSCIMAuth(verify, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/scim/v2/Users", nil)
	req = req.WithContext(context.WithValue(req.Context(), k, "value-123"))
	req.Header.Set("Authorization", "Bearer scim.good")
	mux.ServeHTTP(httptest.NewRecorder(), req)

	if got != "value-123" {
		t.Fatalf("verifier did not receive the request context: got %v", got)
	}
}

// Fix 3 — a PUT with no `active` field must NOT deactivate the user.
func TestSCIMPut_missingActive_doesNotDisable(t *testing.T) {
	h, tc, _ := newSCIMUsersTestHandler(t)

	// Provision an active user.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, scimReq(http.MethodPost, "/scim/v2/Users",
		`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"puser","externalId":"ext-p","emails":[{"value":"p@x.io","primary":true}],"active":true}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST: want 201, got %d: %s", rec.Code, rec.Body)
	}
	var created scimUser
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	// PUT a body WITHOUT `active`.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, scimReq(http.MethodPut, "/scim/v2/Users/"+created.ID,
		`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"puser","displayName":"P User"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: want 200, got %d: %s", rec.Code, rec.Body)
	}
	if u := tc.users.users["puser"]; u == nil || !u.IsActive {
		t.Error("PUT without `active` must NOT disable the user")
	}
}

// Fix 4 — GET /scim/v2/Users/{id} after a provision must echo the non-empty
// externalId (Okta/Entra reconciliation key). Also covers the PUT response.
func TestSCIMGetByID_echoesExternalID(t *testing.T) {
	h, _, _ := newSCIMUsersTestHandler(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, scimReq(http.MethodPost, "/scim/v2/Users",
		`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"euser","externalId":"ext-echo","emails":[{"value":"e@x.io","primary":true}],"active":true}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST: want 201, got %d: %s", rec.Code, rec.Body)
	}
	var created scimUser
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, scimReq(http.MethodGet, "/scim/v2/Users/"+created.ID, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET by id: want 200, got %d: %s", rec.Code, rec.Body)
	}
	var got scimUser
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if got.ExternalID != "ext-echo" {
		t.Fatalf("GET by id must echo externalId: want %q, got %q", "ext-echo", got.ExternalID)
	}
}

// Fix 5 — PATCH active:true must re-enable a disabled user (the true→re-enable
// direction; the false→disable direction is covered by TestSCIMUsers_lifecycle).
func TestSCIMPatch_activeTrue_reEnables(t *testing.T) {
	h, tc, _ := newSCIMUsersTestHandler(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, scimReq(http.MethodPost, "/scim/v2/Users",
		`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"ruser","externalId":"ext-r","emails":[{"value":"r@x.io","primary":true}],"active":true}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST: want 201, got %d: %s", rec.Code, rec.Body)
	}
	var created scimUser
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	// Disable first.
	disable := `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"active","value":false}]}`
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, scimReq(http.MethodPatch, "/scim/v2/Users/"+created.ID, disable))
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH active:false: want 200, got %d", rec.Code)
	}
	if u := tc.users.users["ruser"]; u == nil || u.IsActive {
		t.Fatalf("precondition: user should be disabled")
	}

	// Re-enable.
	enable := `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"active","value":true}]}`
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, scimReq(http.MethodPatch, "/scim/v2/Users/"+created.ID, enable))
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH active:true: want 200, got %d: %s", rec.Code, rec.Body)
	}
	if u := tc.users.users["ruser"]; u == nil || !u.IsActive {
		t.Error("PATCH active:true must re-enable the user")
	}
}

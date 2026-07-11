//go:build integration

// Package handler scim_integration_test.go exercises the full SCIM Users
// lifecycle against a real PostgreSQL (migrations applied) + Redis, driving the
// HTTP surface end-to-end: token gate → provision → list-by-filter → active
// toggle → local-password-collision 409. It asserts provisioned users land
// under the bootstrap tenant and hold a reader@* grant.
package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/redis/go-redis/v9"

	"github.com/steveokay/oci-janus/libs/crypto/argon2"
	"github.com/steveokay/oci-janus/libs/testutil/containers"
	"github.com/steveokay/oci-janus/libs/testutil/fixtures"
	"github.com/steveokay/oci-janus/services/auth/internal/handler"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
	authmigrations "github.com/steveokay/oci-janus/services/auth/migrations"
)

// scimTestEnv bundles the running server + the pieces the SCIM lifecycle test
// asserts against.
type scimTestEnv struct {
	srv      *httptest.Server
	pool     *pgxpool.Pool
	users    *repository.UserRepository
	tenantID uuid.UUID
	token    string // raw SCIM bearer token
}

// newSCIMTestEnv wires real Postgres + Redis, applies migrations, seeds a SCIM
// token, and returns an httptest.Server serving the SCIM routes.
func newSCIMTestEnv(t *testing.T) *scimTestEnv {
	t.Helper()
	ctx := context.Background()

	dsn := containers.Postgres(t)
	redisAddr := containers.Redis(t)

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to test postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	sqlDB := stdlib.OpenDB(*poolCfg.ConnConfig)
	t.Cleanup(func() { _ = sqlDB.Close() })
	goose.SetBaseFS(authmigrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set goose dialect: %v", err)
	}
	if err := goose.Up(sqlDB, "."); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	privB64, pubB64, err := fixtures.RSAKeyPair()
	if err != nil {
		t.Fatalf("generate rsa key pair: %v", err)
	}

	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	t.Cleanup(func() { _ = rdb.Close() })

	users := repository.NewUserRepository(pool)
	apiKeys := repository.NewAPIKeyRepository(pool)
	sa := repository.NewServiceAccountRepo(pool)
	svc, err := service.New(users, apiKeys, sa, nil, rdb, privB64, pubB64, "test-key-1")
	if err != nil {
		t.Fatalf("init auth service: %v", err)
	}

	// The SCIM bootstrap tenant. Use a fresh tenant id; the seed migration's
	// dev tenant is unrelated to SCIM.
	tenantID := uuid.New()
	svc.SetSCIMRepo(users, tenantID)

	// Seed a SCIM token directly via the repo: store its Argon2 hash + enable.
	rawToken := "scim." + uuid.NewString()
	hash, err := argon2.Hash(rawToken)
	if err != nil {
		t.Fatalf("hash scim token: %v", err)
	}
	if err := users.UpsertSCIMToken(ctx, tenantID, hash, true); err != nil {
		t.Fatalf("seed scim token: %v", err)
	}

	mux := http.NewServeMux()
	h := handler.NewHTTPHandler(svc, tenantID)
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &scimTestEnv{srv: srv, pool: pool, users: users, tenantID: tenantID, token: rawToken}
}

func (e *scimTestEnv) do(t *testing.T, method, path, token, body string) *http.Response {
	t.Helper()
	var r *http.Request
	var err error
	if body == "" {
		r, err = http.NewRequest(method, e.srv.URL+path, nil)
	} else {
		r, err = http.NewRequest(method, e.srv.URL+path, bytes.NewReader([]byte(body)))
	}
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Content-Type", "application/scim+json")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func TestSCIM_Lifecycle(t *testing.T) {
	env := newSCIMTestEnv(t)
	ctx := context.Background()

	// 1. Token gate: wrong token → 401, right token accepted (via a later 200).
	resp := env.do(t, http.MethodGet, "/scim/v2/Users", "scim.wrong-token", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: want 401, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = env.do(t, http.MethodGet, "/scim/v2/Users", "", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: want 401, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 2. POST a new user → 201; assert passwordless, external_id, reader@* grant.
	createBody := `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"alice","externalId":"okta-ext-1","emails":[{"value":"alice@corp.io","primary":true}],"active":true}`
	resp = env.do(t, http.MethodPost, "/scim/v2/Users", env.token, createBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /Users: want 201, got %d", resp.StatusCode)
	}
	var created struct {
		ID         string `json:"id"`
		UserName   string `json:"userName"`
		ExternalID string `json:"externalId"`
		Active     bool   `json:"active"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	resp.Body.Close()
	if created.ID == "" || created.ExternalID != "okta-ext-1" || !created.Active {
		t.Fatalf("created resource mismatch: %+v", created)
	}
	createdID := uuid.MustParse(created.ID)

	// DB assertions: bootstrap tenant, passwordless, external_id set.
	dbUser, err := env.users.GetUserByExternalID(ctx, env.tenantID, "okta-ext-1")
	if err != nil {
		t.Fatalf("GetUserByExternalID: %v", err)
	}
	if dbUser.TenantID != env.tenantID {
		t.Errorf("provisioned user must land under the bootstrap tenant: got %s want %s", dbUser.TenantID, env.tenantID)
	}
	if dbUser.PasswordHash != "" {
		t.Error("provisioned user must be passwordless")
	}
	if dbUser.ID != createdID {
		t.Error("db user id must match the created resource id")
	}

	// reader@org:* grant present.
	roles, err := env.users.GetUserRoles(ctx, createdID, env.tenantID)
	if err != nil {
		t.Fatalf("GetUserRoles: %v", err)
	}
	if !hasReaderStar(roles) {
		t.Errorf("provisioned user must hold reader@org:* , got %+v", roles)
	}

	// 3. GET list by userName filter → 1 result; by externalId → 1 result.
	assertOneResult(t, env, `userName eq "alice"`)
	assertOneResult(t, env, `externalId eq "okta-ext-1"`)

	// 4. PATCH active:false → 200; assert is_active=false in the DB.
	patchOff := `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"active","value":false}]}`
	resp = env.do(t, http.MethodPatch, "/scim/v2/Users/"+created.ID, env.token, patchOff)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH active:false: want 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()
	if active := dbActive(t, env, createdID); active {
		t.Error("PATCH active:false must disable the user in the DB")
	}

	// 5. PATCH active:true → 200; assert re-enabled.
	patchOn := `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"active","value":true}]}`
	resp = env.do(t, http.MethodPatch, "/scim/v2/Users/"+created.ID, env.token, patchOn)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH active:true: want 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()
	if active := dbActive(t, env, createdID); !active {
		t.Error("PATCH active:true must re-enable the user")
	}

	// 6. POST a user whose email matches an existing LOCAL-password account → 409.
	// Seed a local-password user first.
	if _, err := env.pool.Exec(ctx, `
		INSERT INTO users (tenant_id, username, email, password_hash, kind)
		VALUES ($1, 'localbob', 'bob@corp.io', '$argon2id$notempty', 'human')`,
		env.tenantID); err != nil {
		t.Fatalf("seed local-password user: %v", err)
	}
	collisionBody := `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"bob2","externalId":"okta-ext-2","emails":[{"value":"bob@corp.io","primary":true}],"active":true}`
	resp = env.do(t, http.MethodPost, "/scim/v2/Users", env.token, collisionBody)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("local-password collision: want 409, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func hasReaderStar(roles []repository.RoleAssignment) bool {
	for _, r := range roles {
		if r.RoleName == "reader" && r.ScopeType == "org" && r.ScopeValue == "*" {
			return true
		}
	}
	return false
}

func assertOneResult(t *testing.T, env *scimTestEnv, filter string) {
	t.Helper()
	q := "/scim/v2/Users?filter=" + urlQueryEscape(filter)
	resp := env.do(t, http.MethodGet, q, env.token, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET list %q: want 200, got %d", filter, resp.StatusCode)
	}
	var list struct {
		TotalResults int `json:"totalResults"`
		Resources    []struct {
			UserName string `json:"userName"`
		} `json:"Resources"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode list %q: %v", filter, err)
	}
	if list.TotalResults != 1 || len(list.Resources) != 1 {
		t.Fatalf("filter %q: want exactly 1 result, got total=%d len=%d", filter, list.TotalResults, len(list.Resources))
	}
}

func dbActive(t *testing.T, env *scimTestEnv, id uuid.UUID) bool {
	t.Helper()
	var active bool
	if err := env.pool.QueryRow(context.Background(),
		`SELECT is_active FROM users WHERE id = $1`, id).Scan(&active); err != nil {
		t.Fatalf("query is_active: %v", err)
	}
	return active
}

// urlQueryEscape escapes just the characters our filter tests use so the query
// stays readable in the assertions above.
func urlQueryEscape(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch r {
		case ' ':
			out = append(out, '%', '2', '0')
		case '"':
			out = append(out, '%', '2', '2')
		default:
			out = append(out, r)
		}
	}
	return string(out)
}

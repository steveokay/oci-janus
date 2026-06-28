//go:build integration

// Package integration contains end-to-end HTTP tests for registry-auth.
// Each test runs against a real PostgreSQL + Redis instance spun up via testcontainers.
package integration

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

	"github.com/steveokay/oci-janus/libs/testutil/containers"
	"github.com/steveokay/oci-janus/libs/testutil/fixtures"
	"github.com/steveokay/oci-janus/services/auth/internal/handler"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
	authmigrations "github.com/steveokay/oci-janus/services/auth/migrations"
)

const devTenantID = "98dbe36b-ef28-4903-b25c-bff1b2921c9e"

// testEnv holds the shared HTTP server, a pre-created test user, and tenant ID.
type testEnv struct {
	srv      *httptest.Server
	tenantID uuid.UUID
	// testUsername and testPassword are a freshly created user for each test run.
	testUsername string
	testPassword string
}

// newTestEnv wires up real Postgres + Redis containers, runs migrations, and
// returns an httptest.Server backed by the full auth service stack.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	ctx := context.Background()
	dsn := containers.Postgres(t)
	redisAddr := containers.Redis(t)

	// Run goose migrations against the test database so the schema is current.
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

	// Generate a fresh RSA key pair so the service can sign and verify tokens.
	privB64, pubB64, err := fixtures.RSAKeyPair()
	if err != nil {
		t.Fatalf("generate rsa key pair: %v", err)
	}

	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	t.Cleanup(func() { _ = rdb.Close() })

	users := repository.NewUserRepository(pool)
	apiKeys := repository.NewAPIKeyRepository(pool)
	sa := repository.NewServiceAccountRepo(pool)
	// audit is nil in integration tests; cross-tenant audit emission is
	// a service-layer unit-test concern (TestValidateAPIKey_CrossTenantGuard_T5).
	svc, err := service.New(users, apiKeys, sa, nil, rdb, privB64, pubB64, "test-key-1")
	if err != nil {
		t.Fatalf("init auth service: %v", err)
	}

	tenantID := uuid.MustParse(devTenantID)
	mux := http.NewServeMux()
	handler.NewHTTPHandler(svc, tenantID).Register(mux)

	// Pre-create a test user so individual tests don't need to set one up.
	const (
		testUsername = "integration-test-user"
		testPassword = "IntTest@Pass1234"
	)
	_, err = svc.CreateUser(ctx, tenantID, testUsername, "inttest@example.com", "", testPassword)
	if err != nil {
		t.Fatalf("create test user: %v", err)
	}

	return &testEnv{
		srv:          httptest.NewServer(mux),
		tenantID:     tenantID,
		testUsername: testUsername,
		testPassword: testPassword,
	}
}

// TestToken_DockerFlow tests the Docker token auth endpoint:
// valid credentials return a signed JWT, invalid credentials return 401.
func TestToken_DockerFlow(t *testing.T) {
	env := newTestEnv(t)
	defer env.srv.Close()

	t.Run("valid credentials return JWT", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/auth/token?scope=repository:org/repo:pull", nil)
		req.SetBasicAuth(env.testUsername, env.testPassword)
		req.Header.Set("X-Tenant-ID", env.tenantID.String())

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("want 200, got %d", resp.StatusCode)
		}
		var body struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if body.Token == "" {
			t.Fatal("expected non-empty token")
		}
	})

	t.Run("wrong password returns 401", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/auth/token", nil)
		req.SetBasicAuth(env.testUsername, "wrongpassword")
		req.Header.Set("X-Tenant-ID", env.tenantID.String())

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d", resp.StatusCode)
		}
	})

	t.Run("no credentials returns 401", func(t *testing.T) {
		// A request with no Basic auth must be rejected.
		req, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/auth/token", nil)
		req.Header.Set("X-Tenant-ID", env.tenantID.String())

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d", resp.StatusCode)
		}
	})
}

// TestJWKS verifies the public key set endpoint returns a valid RSA key.
func TestJWKS(t *testing.T) {
	env := newTestEnv(t)
	defer env.srv.Close()

	resp, err := http.Get(env.srv.URL + "/.well-known/jwks.json")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	var body struct {
		Keys []struct {
			Kty string `json:"kty"`
			Alg string `json:"alg"`
			Kid string `json:"kid"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Keys) == 0 {
		t.Fatal("expected at least one key in JWKS")
	}
	if body.Keys[0].Kty != "RSA" {
		t.Fatalf("want kty=RSA, got %s", body.Keys[0].Kty)
	}
	if body.Keys[0].Alg != "RS256" {
		t.Fatalf("want alg=RS256, got %s", body.Keys[0].Alg)
	}
}

// TestCreateUser_AndLogin verifies that a newly created user can immediately log in.
func TestCreateUser_AndLogin(t *testing.T) {
	env := newTestEnv(t)
	defer env.srv.Close()

	const (
		username = "testuser"
		password = "TestP@ss123!"
		email    = "testuser@example.com"
	)

	// Create user.
	createBody, _ := json.Marshal(map[string]string{
		"tenant_id": env.tenantID.String(),
		"username":  username,
		"email":     email,
		"password":  password,
	})
	resp, err := http.Post(env.srv.URL+"/api/v1/users", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create user request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("want 201, got %d", resp.StatusCode)
	}

	// Log in with the new user's credentials.
	loginBody, _ := json.Marshal(map[string]string{
		"tenant_id": env.tenantID.String(),
		"username":  username,
		"password":  password,
	})
	resp2, err := http.Post(env.srv.URL+"/api/v1/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("want 200 on login, got %d", resp2.StatusCode)
	}

	var loginResp struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&loginResp); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if loginResp.Token == "" {
		t.Fatal("expected non-empty login token")
	}
}

// TestAPIKey_CreateListDelete tests the full API key lifecycle.
func TestAPIKey_CreateListDelete(t *testing.T) {
	env := newTestEnv(t)
	defer env.srv.Close()

	// Log in to get a session token.
	loginBody, _ := json.Marshal(map[string]string{
		"tenant_id": env.tenantID.String(),
		"username":  env.testUsername,
		"password":  env.testPassword,
	})
	resp, err := http.Post(env.srv.URL+"/api/v1/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: want 200, got %d", resp.StatusCode)
	}
	var loginResp struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&loginResp); err != nil {
		t.Fatalf("decode login response: %v", err)
	}

	// Create an API key.
	keyBody, _ := json.Marshal(map[string]any{
		"name":   "ci-robot",
		"scopes": []string{"push", "pull"},
	})
	req, _ := http.NewRequest(http.MethodPost, env.srv.URL+"/api/v1/apikeys", bytes.NewReader(keyBody))
	req.Header.Set("Authorization", "Bearer "+loginResp.Token)
	req.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create api key request failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("create api key: want 201, got %d", resp2.StatusCode)
	}

	var keyResp struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&keyResp); err != nil {
		t.Fatalf("decode api key response: %v", err)
	}
	if keyResp.Key == "" {
		t.Fatal("expected raw key in create response")
	}

	// List API keys.
	listReq, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/api/v1/apikeys", nil)
	listReq.Header.Set("Authorization", "Bearer "+loginResp.Token)
	resp3, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatalf("list api keys request failed: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("list api keys: want 200, got %d", resp3.StatusCode)
	}

	// Delete the API key.
	delReq, _ := http.NewRequest(http.MethodDelete, env.srv.URL+"/api/v1/apikeys/"+keyResp.ID, nil)
	delReq.Header.Set("Authorization", "Bearer "+loginResp.Token)
	resp4, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("delete api key request failed: %v", err)
	}
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusNoContent {
		t.Fatalf("delete api key: want 204, got %d", resp4.StatusCode)
	}
}

// TestLogout_RevokesToken verifies that a token cannot be used after logout.
func TestLogout_RevokesToken(t *testing.T) {
	env := newTestEnv(t)
	defer env.srv.Close()

	// Log in.
	loginBody, _ := json.Marshal(map[string]string{
		"tenant_id": env.tenantID.String(),
		"username":  env.testUsername,
		"password":  env.testPassword,
	})
	resp, err := http.Post(env.srv.URL+"/api/v1/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	defer resp.Body.Close()
	var loginResp struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&loginResp)

	// Logout.
	logoutReq, _ := http.NewRequest(http.MethodPost, env.srv.URL+"/api/v1/logout", nil)
	logoutReq.Header.Set("Authorization", "Bearer "+loginResp.Token)
	resp2, err := http.DefaultClient.Do(logoutReq)
	if err != nil {
		t.Fatalf("logout request failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: want 204, got %d", resp2.StatusCode)
	}

	// Attempting to list API keys with the revoked token must fail with 401.
	listReq, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/api/v1/apikeys", nil)
	listReq.Header.Set("Authorization", "Bearer "+loginResp.Token)
	resp3, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatalf("list api keys request failed: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusUnauthorized {
		t.Fatalf("post-logout request: want 401, got %d", resp3.StatusCode)
	}
}

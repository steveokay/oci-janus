// Package bootstrap implements the `registry-auth bootstrap` CLI subcommand.
// It creates the first tenant and first platform-admin user in a fresh
// deployment, replacing the dev-seed admin migrations that shipped a known
// password hash (REDESIGN-001 Phase 3.1.b / Top-5 #5 security fix).
//
// Entry point: Run(ctx, args, stdin, stdout). The thin os.Args dispatch in
// cmd/server/main.go calls Run and converts the returned error into an exit
// code; tests call Run directly with controlled stdin/stdout without spawning
// a subprocess.
package bootstrap

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	argon2pkg "github.com/steveokay/oci-janus/libs/crypto/argon2"
)

// ValidationError is returned when the caller supplies invalid arguments or
// input (missing flags, bad format, empty password, etc.). The main.go
// dispatch converts a ValidationError into exit code 2 so operators can
// distinguish operator-error (2) from infrastructure failure (1).
type ValidationError struct {
	msg string
}

// Error implements the error interface.
func (e *ValidationError) Error() string { return e.msg }

// validationError wraps a message in a *ValidationError for clean sentinel
// matching via errors.As.
func validationError(format string, args ...any) *ValidationError {
	return &ValidationError{msg: fmt.Sprintf(format, args...)}
}

// usernameRE is the canonical username pattern from CLAUDE.md §7 input
// validation. Bootstrap applies the same rule as the normal user-creation
// path so the created admin is subject to identical constraints.
var usernameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{3,64}$`)

// nonAlphanumRE matches any run of characters that are not lowercase
// ASCII letters or digits. Used by normalizeSlug to replace them with '-'.
var nonAlphanumRE = regexp.MustCompile(`[^a-z0-9]+`)

// repeatedDashRE matches two or more consecutive dashes.
var repeatedDashRE = regexp.MustCompile(`-{2,}`)

// normalizeSlug converts a tenant name to a DNS-safe slug, mirroring the
// logic in services/tenant/internal/repository.NormalizeSlug and the SQL
// backfill in 20260620000001_add_tenant_slug.sql:
//
//  1. Lowercase all letters.
//  2. Replace runs of non-alphanumeric characters with a single '-'.
//  3. Collapse repeated '-' to one '-'.
//  4. Trim leading and trailing '-'.
//
// If the result is empty (e.g. the name contained only non-ASCII characters)
// the tenant UUID is used as a fallback so the NOT NULL constraint is
// satisfied. The fallback is also what the SQL migration does.
func normalizeSlug(name string, fallbackID uuid.UUID) string {
	// Step 1: lowercase.
	lower := strings.ToLower(name)

	// Step 2: replace non-alphanumeric runs with '-'.
	dashed := nonAlphanumRE.ReplaceAllString(lower, "-")

	// Step 3: collapse repeated dashes.
	collapsed := repeatedDashRE.ReplaceAllString(dashed, "-")

	// Step 4: trim leading/trailing dashes.
	slug := strings.Trim(collapsed, "-")

	if slug == "" {
		return fallbackID.String()
	}
	return slug
}

// Config holds the parsed, validated arguments and environment values for a
// single bootstrap run. It is separated from flag parsing so tests can
// construct it directly without needing real CLI flags or environment variables.
type Config struct {
	// AdminEmail is the email address for the first admin user. Must contain
	// '@' and no whitespace (rough RFC-5321 sanity check).
	AdminEmail string
	// AdminUsername is the login name for the first admin user. Validated
	// against usernameRE (^[a-zA-Z0-9_-]{3,64}$).
	AdminUsername string
	// TenantName is the human-readable name for the first tenant. Non-empty.
	TenantName string
	// TenantID is the UUID to use for the new tenant. If zero (uuid.Nil), Run
	// generates a fresh one via uuid.New().
	TenantID uuid.UUID
	// AuthDBDSN is the Postgres DSN for the auth schema (AUTH_DB_DSN).
	AuthDBDSN string
	// TenantDBDSN is the Postgres DSN for the tenant schema (TENANT_DB_DSN).
	TenantDBDSN string
	// DeploymentMode controls single-vs-multi bootstrap idempotency.
	// Valid values: "single" (default), "multi".
	DeploymentMode string
}

// Run is the bootstrap CLI entry point. It parses flags from args, reads the
// password from stdin, and drives the full bootstrap sequence against the
// Postgres databases identified in the environment. On success it writes a
// human-readable summary to stdout and returns nil.
//
// args should be os.Args[2:] (i.e. everything after "bootstrap"). stdin and
// stdout are injectable for testing; use os.Stdin / os.Stdout in production.
//
// Error classification:
//   - *ValidationError → caller should exit 2 (operator input problem)
//   - any other error  → caller should exit 1 (infrastructure / internal)
func Run(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	cfg, err := parseArgs(args)
	if err != nil {
		return err // already a *ValidationError
	}

	// Read the password from stdin before touching any DB so a missing pipe
	// fails fast with exit 2 rather than reaching a partial DB state.
	password, err := readPasswordFromStdin(stdin)
	if err != nil {
		return err // *ValidationError or io error
	}

	return runWithConfig(ctx, cfg, password, stdout)
}

// RunWithConfig allows tests to bypass flag parsing and stdin reading entirely,
// driving the bootstrap sequence with a pre-built Config and a plaintext
// password string. This is the primary test surface — no subprocess, no stdin
// piping boilerplate.
func RunWithConfig(ctx context.Context, cfg Config, password string, stdout io.Writer) error {
	return runWithConfig(ctx, cfg, password, stdout)
}

// runWithConfig is the shared implementation called by both Run and
// RunWithConfig.
func runWithConfig(ctx context.Context, cfg Config, password string, stdout io.Writer) error {
	// ── 1. Validate config fields ─────────────────────────────────────────────

	if err := validateConfig(cfg); err != nil {
		return err
	}

	// Validate the password separately (not in validateConfig) so the test that
	// checks hash correctness can supply a real password directly.
	if err := validatePassword(password); err != nil {
		return err
	}

	// ── 2. Resolve tenant ID ──────────────────────────────────────────────────

	tenantID := cfg.TenantID
	if tenantID == uuid.Nil {
		tenantID = uuid.New()
		slog.Info("bootstrap: generated tenant ID", "tenant_id", tenantID)
	}

	// ── 3. Connect to tenant DB ───────────────────────────────────────────────

	tenantPool, err := pgxpool.New(ctx, cfg.TenantDBDSN)
	if err != nil {
		return fmt.Errorf("connect tenant DB: %w", err)
	}
	defer tenantPool.Close()

	if err := tenantPool.Ping(ctx); err != nil {
		return fmt.Errorf("ping tenant DB: %w", err)
	}

	// ── 4. Idempotency check on the tenant DB ─────────────────────────────────

	if err := checkIdempotency(ctx, tenantPool, tenantID, cfg.DeploymentMode); err != nil {
		return err
	}

	// ── 5. Write tenant rows ──────────────────────────────────────────────────

	if err := writeTenant(ctx, tenantPool, tenantID, cfg.TenantName); err != nil {
		return fmt.Errorf("write tenant: %w", err)
	}

	// ── 6. Connect to auth DB ─────────────────────────────────────────────────

	authPool, err := pgxpool.New(ctx, cfg.AuthDBDSN)
	if err != nil {
		return fmt.Errorf("connect auth DB: %w", err)
	}
	defer authPool.Close()

	if err := authPool.Ping(ctx); err != nil {
		return fmt.Errorf("ping auth DB: %w", err)
	}

	// ── 7. Check for existing admin ───────────────────────────────────────────

	exists, err := findExistingAdmin(ctx, authPool, tenantID, cfg.AdminEmail)
	if err != nil {
		return fmt.Errorf("check existing admin: %w", err)
	}
	if exists {
		return validationError(
			"admin already exists for this tenant + email (%s); use a different email or revoke the existing admin out-of-band",
			cfg.AdminEmail,
		)
	}

	// ── 8. Hash password ──────────────────────────────────────────────────────

	// Hashing happens after the admin-existence check so we don't pay the
	// ~100 ms argon2id cost on an error path. The password is never logged.
	passwordHash, err := argon2pkg.Hash(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	// ── 9. Write user + platform-admin marker in auth DB ─────────────────────

	adminUserID, err := writeAdmin(ctx, authPool, tenantID, cfg.AdminUsername, cfg.AdminEmail, passwordHash)
	if err != nil {
		return fmt.Errorf("write admin: %w", err)
	}

	// ── 10. Print summary ─────────────────────────────────────────────────────

	fmt.Fprintf(stdout, "Bootstrap complete.\n")
	fmt.Fprintf(stdout, "Tenant ID:     %s\n", tenantID)
	fmt.Fprintf(stdout, "Tenant name:   %s\n", cfg.TenantName)
	fmt.Fprintf(stdout, "Admin user ID: %s\n", adminUserID)
	fmt.Fprintf(stdout, "Admin email:   %s\n", cfg.AdminEmail)

	return nil
}

// ── Flag parsing ──────────────────────────────────────────────────────────────

// parseArgs parses the CLI arguments after "bootstrap" and reads DSNs +
// deployment mode from environment variables.
func parseArgs(args []string) (Config, error) {
	fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // suppress flag's default error output; we wrap it

	adminEmail := fs.String("admin-email", "", "email address for the first admin user (required)")
	adminUsername := fs.String("admin-username", "", "username for the first admin user (required)")
	tenantName := fs.String("tenant-name", "", "human-readable name for the first tenant (required)")
	tenantIDStr := fs.String("tenant-id", "", "UUID for the first tenant (optional; generated if omitted)")
	adminPasswordStdin := fs.Bool("admin-password-stdin", false, "read admin password from stdin (required)")

	if err := fs.Parse(args); err != nil {
		return Config{}, validationError("parse flags: %v", err)
	}

	// Validate required flags.
	var missing []string
	if *adminEmail == "" {
		missing = append(missing, "--admin-email")
	}
	if *adminUsername == "" {
		missing = append(missing, "--admin-username")
	}
	if *tenantName == "" {
		missing = append(missing, "--tenant-name")
	}
	if !*adminPasswordStdin {
		missing = append(missing, "--admin-password-stdin")
	}
	if len(missing) > 0 {
		return Config{}, validationError("missing required flags: %s", strings.Join(missing, ", "))
	}

	// Parse optional --tenant-id.
	var tenantID uuid.UUID
	if *tenantIDStr != "" {
		var err error
		tenantID, err = uuid.Parse(*tenantIDStr)
		if err != nil {
			return Config{}, validationError("--tenant-id is not a valid UUID: %v", err)
		}
	}
	// uuid.Nil means "generate one at runtime" — checked in runWithConfig.

	// Read DSNs and deployment mode from environment.
	authDSN := os.Getenv("AUTH_DB_DSN")
	if authDSN == "" {
		return Config{}, validationError("AUTH_DB_DSN environment variable is required")
	}
	tenantDSN := os.Getenv("TENANT_DB_DSN")
	if tenantDSN == "" {
		return Config{}, validationError("TENANT_DB_DSN environment variable is required")
	}
	deployMode := os.Getenv("DEPLOYMENT_MODE")
	if deployMode == "" {
		deployMode = "single"
	}

	return Config{
		AdminEmail:     *adminEmail,
		AdminUsername:  *adminUsername,
		TenantName:     *tenantName,
		TenantID:       tenantID,
		AuthDBDSN:      authDSN,
		TenantDBDSN:    tenantDSN,
		DeploymentMode: deployMode,
	}, nil
}

// ── Validation ────────────────────────────────────────────────────────────────

// validateConfig applies field-level validation rules to a Config. These same
// rules fire in parseArgs, but re-applying them here means RunWithConfig (the
// test entry point) also enforces them when callers build Config manually.
func validateConfig(cfg Config) error {
	if cfg.AuthDBDSN == "" {
		return validationError("AuthDBDSN is required")
	}
	if cfg.TenantDBDSN == "" {
		return validationError("TenantDBDSN is required")
	}
	if cfg.AdminEmail == "" {
		return validationError("--admin-email is required")
	}
	if !strings.Contains(cfg.AdminEmail, "@") || strings.ContainsAny(cfg.AdminEmail, " \t\n\r") {
		return validationError("--admin-email does not look like a valid email address")
	}
	if cfg.AdminUsername == "" {
		return validationError("--admin-username is required")
	}
	if !usernameRE.MatchString(cfg.AdminUsername) {
		return validationError("--admin-username must match ^[a-zA-Z0-9_-]{3,64}$")
	}
	if cfg.TenantName == "" {
		return validationError("--tenant-name is required")
	}
	mode := cfg.DeploymentMode
	if mode == "" {
		mode = "single"
	}
	if mode != "single" && mode != "multi" {
		return validationError("DEPLOYMENT_MODE must be 'single' or 'multi', got %q", mode)
	}
	return nil
}

// validatePassword ensures the password is non-empty and contains no internal
// newlines (multi-line passwords are an operator mistake that stdin trimming
// would silently truncate).
func validatePassword(password string) error {
	if password == "" {
		return validationError("password must not be empty (supply via --admin-password-stdin)")
	}
	if strings.ContainsAny(password, "\n\r") {
		return validationError("password must be a single line (no embedded newlines)")
	}
	return nil
}

// readPasswordFromStdin reads exactly one line from r, trims a trailing
// CR/LF, and rejects empty input or multi-line input.
func readPasswordFromStdin(r io.Reader) (string, error) {
	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("read password from stdin: %w", err)
		}
		// EOF without any bytes → empty stdin.
		return "", validationError("password must not be empty (supply via --admin-password-stdin)")
	}
	password := strings.TrimRight(scanner.Text(), "\r\n")

	// If there is a second line the operator piped a multi-line file — reject.
	if scanner.Scan() {
		return "", validationError(
			"password must be a single line (no embedded newlines); got multi-line input from stdin",
		)
	}

	if err := validatePassword(password); err != nil {
		return "", err
	}
	return password, nil
}

// ── Tenant DB operations ──────────────────────────────────────────────────────

// checkIdempotency queries deployment_metadata to determine whether a previous
// bootstrap has already run and whether the current call is permitted.
//
// Rules:
//   - No row found → fresh deployment, proceed unconditionally.
//   - Row found + single mode + same tenant ID → idempotent re-bootstrap, proceed.
//   - Row found + single mode + different tenant ID → error (only one tenant allowed).
//   - Row found + multi mode → always proceed (multi supports additional tenants).
func checkIdempotency(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, mode string) error {
	var rawValue string
	err := pool.QueryRow(ctx,
		`SELECT value::text FROM deployment_metadata WHERE key = 'bootstrap_tenant_id'`,
	).Scan(&rawValue)

	if errors.Is(err, pgx.ErrNoRows) {
		// Fresh deployment — unconditionally proceed.
		return nil
	}
	if err != nil {
		return fmt.Errorf("query deployment_metadata: %w", err)
	}

	// Strip the surrounding JSON string quotes from the JSONB-cast-to-text value.
	// The stored JSONB is a JSON string ("\"<uuid>\"") so ::text gives us "\"<uuid>\"".
	recorded := strings.Trim(rawValue, `"`)

	deployMode := mode
	if deployMode == "" {
		deployMode = "single"
	}

	switch deployMode {
	case "single":
		if recorded == tenantID.String() {
			// Same tenant ID — safe to replay (idempotent).
			slog.Info("bootstrap: idempotent re-bootstrap detected (same tenant ID)", "tenant_id", tenantID)
			return nil
		}
		return validationError(
			"deployment already bootstrapped (DEPLOYMENT_MODE=single allows one bootstrap_tenant_id); recorded=%s, requested=%s",
			recorded, tenantID,
		)
	case "multi":
		// Multi-mode supports multiple tenants — no restriction.
		slog.Info("bootstrap: DEPLOYMENT_MODE=multi, proceeding with additional tenant", "tenant_id", tenantID)
		return nil
	default:
		return validationError("unknown DEPLOYMENT_MODE %q", deployMode)
	}
}

// writeTenant inserts the tenant row, its policy defaults, and the
// deployment_metadata sentinel in a single transaction so a crash mid-way
// leaves the DB in a clean state.
func writeTenant(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, tenantName string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tenant tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit

	// (a) Insert tenant row — idempotent via ON CONFLICT DO NOTHING so a
	// re-bootstrap with the same tenant ID doesn't fail.
	// slug is derived from the tenant name using the same normalization logic
	// as the SQL backfill migration (20260620000001_add_tenant_slug.sql) and
	// the Go-side NormalizeSlug function in services/tenant. The column is NOT
	// NULL after that migration so we must supply it here.
	slug := normalizeSlug(tenantName, tenantID)
	_, err = tx.Exec(ctx,
		`INSERT INTO tenants (id, name, slug, plan)
		 VALUES ($1, $2, $3, 'standard')
		 ON CONFLICT (id) DO NOTHING`,
		tenantID, tenantName, slug,
	)
	if err != nil {
		return fmt.Errorf("insert tenant: %w", err)
	}

	// (b) Insert tenant_policies with schema defaults — idempotent.
	_, err = tx.Exec(ctx,
		`INSERT INTO tenant_policies (tenant_id)
		 VALUES ($1)
		 ON CONFLICT (tenant_id) DO NOTHING`,
		tenantID,
	)
	if err != nil {
		return fmt.Errorf("insert tenant_policies: %w", err)
	}

	// (c) Record the bootstrap sentinel. ON CONFLICT DO NOTHING keeps this
	// idempotent for re-bootstrap scenarios where the row already exists.
	tenantIDJSON, err := json.Marshal(tenantID.String())
	if err != nil {
		return fmt.Errorf("marshal tenant ID to JSON: %w", err)
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO deployment_metadata (key, value)
		 VALUES ('bootstrap_tenant_id', $1::jsonb)
		 ON CONFLICT (key) DO NOTHING`,
		string(tenantIDJSON),
	)
	if err != nil {
		return fmt.Errorf("insert deployment_metadata: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tenant tx: %w", err)
	}

	slog.Info("bootstrap: tenant written", "tenant_id", tenantID, "tenant_name", tenantName)
	return nil
}

// ── Auth DB operations ────────────────────────────────────────────────────────

// findExistingAdmin reports whether a user with the given email already exists
// in the given tenant. Returns (true, nil) if found, (false, nil) if not found,
// or (false, err) on a DB error.
func findExistingAdmin(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, email string) (bool, error) {
	var userID uuid.UUID
	err := pool.QueryRow(ctx,
		`SELECT id FROM users WHERE tenant_id = $1 AND email = $2`,
		tenantID, email,
	).Scan(&userID)

	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query users: %w", err)
	}
	return true, nil
}

// writeAdmin inserts the admin user and sets users.is_global_admin = true in
// a single transaction. It returns the new user's UUID.
//
// REDESIGN-001 Phase 5.1: the legacy (admin, org, '*') role_assignments marker
// has been replaced by users.is_global_admin. The bootstrap sets the typed
// column directly; no role_assignments row is inserted. Callers on the login
// path will find is_global_admin=true in the users row and embed the flag in
// the JWT (service.Claims.IsGlobalAdmin).
//
// We intentionally skip org-scoped grants here — the operator can grant those
// via the FE after first login. The is_global_admin flag alone gives access to
// every platform-admin route through h.effectiveGlobalAdmin in services/management.
//
// Pre-condition: the Phase 5.1 migration (20260629000001_users_is_global_admin.sql)
// must already be applied. If the column does not exist, the INSERT will fail
// and the error will surface as an infrastructure problem (not a ValidationError).
func writeAdmin(
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID uuid.UUID,
	username, email, passwordHash string,
) (uuid.UUID, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("begin auth tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Insert the user row with is_global_admin=true.
	// - kind='human' is explicit even though the column defaults to 'human' so
	//   the intent is visible at the call site.
	// - status='active' is explicit for the same reason (default is 'active').
	// - is_global_admin=true is the Phase 5.1 typed platform-admin primitive.
	//   No role_assignments row is inserted for the legacy (admin, org, '*')
	//   marker — that convention is retired by migration 20260629000001.
	// - display_name defaults to NULL; the user can set it later via the FE.
	var adminUserID uuid.UUID
	err = tx.QueryRow(ctx,
		`INSERT INTO users
		     (tenant_id, username, email, password_hash, kind, status, is_global_admin)
		 VALUES ($1, $2, $3, $4, 'human', 'active', TRUE)
		 RETURNING id`,
		tenantID, username, email, passwordHash,
	).Scan(&adminUserID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert admin user: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, fmt.Errorf("commit auth tx: %w", err)
	}

	slog.Info("bootstrap: admin user created with is_global_admin=true",
		"user_id", adminUserID, "tenant_id", tenantID)
	return adminUserID, nil
}

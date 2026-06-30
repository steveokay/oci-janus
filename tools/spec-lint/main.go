// Package main implements `spec-lint` — a CI-runnable check that the claims
// in CLAUDE.md still match the codebase. The tool replaces the failure mode
// where a security/architecture claim drifts from reality without anyone
// noticing until the next review batch.
//
// Each rule is a tuple of (CLAUDE.md sentence/claim, a callable check that
// returns nil on pass or a diagnostic on fail). Add new rules by appending
// to the `rules` slice in main(); keep them small, fast, and deterministic.
//
// REDESIGN-001 Phase 7.3.
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Rule is one CLAUDE.md ↔ code invariant. Description is what the human
// reads in the CI log; check returns nil on pass.
type Rule struct {
	Description string
	Check       func(repoRoot string) error
}

func main() {
	repoRoot := "."
	if len(os.Args) > 1 {
		repoRoot = os.Args[1]
	}
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "spec-lint: resolve repo root: %v\n", err)
		os.Exit(2)
	}

	rules := []Rule{
		{
			Description: "CLAUDE.md §7 'Services reload certs without restart' — libs/auth/mtls must expose GetCertificate-backed reloading config",
			Check:       ruleMTLSHotReload,
		},
		{
			Description: "CLAUDE.md §7 peer-CN allowlist — libs/middleware/grpc must export PeerCNAllowlist + PeerCNAllowlistFromEnv",
			Check:       rulePeerCNAllowlist,
		},
		{
			Description: "CLAUDE.md §7 + Decision #15 — services/audit must have FORCE ROW LEVEL SECURITY on audit_events",
			Check:       ruleAuditForceRLS,
		},
		{
			Description: "CLAUDE.md §7 + Decision #30 — audit_chain_tip table must NOT exist in any migration (the writable tip table was the SEC-050 BLOCKER and got dropped)",
			Check:       ruleNoAuditChainTipTable,
		},
		{
			Description: "CLAUDE.md §10 + Decision #30 — services/audit must declare a chain_seq column and a tip query that orders by it DESC LIMIT 1",
			Check:       ruleAuditChainSeq,
		},
		{
			Description: "CLAUDE.md §7 + Decision #26 — users.is_global_admin column must exist in services/auth migrations",
			Check:       ruleIsGlobalAdminColumn,
		},
		{
			Description: "CLAUDE.md §1 + Decision #28 — deployment_metadata.bootstrap_tenant_id must be wired in services/tenant migrations",
			Check:       ruleBootstrapTenantMetadata,
		},
		{
			Description: "CLAUDE.md §7 + Phase 6.5 — services/auth/internal/service/keyring.go must declare the SEC-048 ring-size cap",
			Check:       ruleKeyRingSizeCap,
		},
		{
			Description: "CLAUDE.md §7 + Decision #29 — libs/crypto/aes must declare a Version constant for the ciphertext prefix",
			Check:       ruleAESVersionConstant,
		},
		{
			Description: "CLAUDE.md §1 — RM-001 removed custom domains: no `tenant_domains` table should exist in services/tenant migrations",
			Check:       ruleNoTenantDomainsTable,
		},
		{
			Description: "CLAUDE.md §10 Audit Trail — every event type in libs/rabbitmq/events must have a case in mapEvent OR a `// audit: skip` annotation",
			Check:       ruleEventCatalogueCovered,
		},
		{
			Description: "CLAUDE.md §7 — every service main.go must call loader.ValidateMTLSConfig before starting servers",
			Check:       ruleEveryServiceValidatesMTLS,
		},
		{
			Description: "CLAUDE.md §10 — every metric promised in §10 must exist in libs/observability/metrics/metrics.go",
			Check:       ruleMetricsExist,
		},
	}

	fmt.Printf("spec-lint: %d rules, root=%s\n", len(rules), absRoot)
	failed := 0
	for _, r := range rules {
		if err := r.Check(absRoot); err != nil {
			failed++
			fmt.Printf("FAIL  %s\n      %v\n", r.Description, err)
			continue
		}
		fmt.Printf("PASS  %s\n", r.Description)
	}
	if failed > 0 {
		fmt.Printf("\nspec-lint: %d of %d rules failed\n", failed, len(rules))
		os.Exit(1)
	}
	fmt.Printf("\nspec-lint: all %d rules passed\n", len(rules))
}

// ── Filesystem helpers ──────────────────────────────────────────────────────

// readFile is a thin os.ReadFile that yields a helpful error for missing
// files (the diagnostic should name the rule's evidence, not stack-trace).
func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// grepFile returns true if the regex matches anywhere in the file. Missing
// file is treated as no-match (not an error) so rules can be lenient about
// platform-specific subtrees if needed.
func grepFile(path string, re *regexp.Regexp) (bool, error) {
	body, err := readFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return re.MatchString(body), nil
}

// grepGlob returns the list of files under `dir` matching `glob` whose
// content matches `re`. Used to assert presence-of-pattern across a tree
// (e.g. "no UPDATE grant on audit_chain_tip in any migration").
func grepGlob(root, dir, glob string, re *regexp.Regexp) ([]string, error) {
	var hits []string
	target := filepath.Join(root, dir)
	walkErr := filepath.WalkDir(target, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// Tolerate missing subtrees — a rule will still fail
				// because the expected positive match won't be there.
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		matched, _ := filepath.Match(glob, filepath.Base(path))
		if !matched {
			return nil
		}
		body, rerr := readFile(path)
		if rerr != nil {
			return rerr
		}
		if re.MatchString(body) {
			hits = append(hits, path)
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.ErrNotExist) {
		return nil, walkErr
	}
	return hits, nil
}

// ── Rules ────────────────────────────────────────────────────────────────────

func ruleMTLSHotReload(root string) error {
	path := filepath.Join(root, "libs", "auth", "mtls", "mtls.go")
	re := regexp.MustCompile(`GetCertificate|GetClientCertificate`)
	ok, err := grepFile(path, re)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("expected GetCertificate or GetClientCertificate wiring in %s — CLAUDE.md claims hot reload via tls.Config.GetCertificate", path)
	}
	return nil
}

func rulePeerCNAllowlist(root string) error {
	path := filepath.Join(root, "libs", "middleware", "grpc", "peer_cn.go")
	body, err := readFile(path)
	if err != nil {
		return fmt.Errorf("expected libs/middleware/grpc/peer_cn.go (Phase 6.10): %w", err)
	}
	for _, want := range []string{"func PeerCNAllowlist(", "func PeerCNAllowlistFromEnv("} {
		if !strings.Contains(body, want) {
			return fmt.Errorf("%s missing %q — CLAUDE.md §7 promises this exported surface", path, want)
		}
	}
	return nil
}

func ruleAuditForceRLS(root string) error {
	migrations := filepath.Join("services", "audit", "migrations")
	re := regexp.MustCompile(`FORCE\s+ROW\s+LEVEL\s+SECURITY`)
	hits, err := grepGlob(root, migrations, "*.sql", re)
	if err != nil {
		return err
	}
	if len(hits) == 0 {
		return fmt.Errorf("no migration under %s declares FORCE ROW LEVEL SECURITY — Decision #15 claims audit_events has it", migrations)
	}
	return nil
}

func ruleNoAuditChainTipTable(root string) error {
	// Allow textual mentions in comments (e.g. the migration documents
	// WHY the table was dropped). Catch real CREATE TABLE re-introductions.
	createRE := regexp.MustCompile(`(?i)CREATE\s+TABLE[^;]*audit_chain_tip`)
	migrations := filepath.Join("services", "audit", "migrations")
	hits, err := grepGlob(root, migrations, "*.sql", createRE)
	if err != nil {
		return err
	}
	if len(hits) > 0 {
		return fmt.Errorf("audit_chain_tip CREATE TABLE found in %v — Decision #30 forbade re-introducing the writable tip table (SEC-050 BLOCKER)", hits)
	}
	return nil
}

func ruleAuditChainSeq(root string) error {
	migrations := filepath.Join("services", "audit", "migrations")
	migrationHits, err := grepGlob(root, migrations, "*.sql", regexp.MustCompile(`chain_seq\s+BIGINT\s+GENERATED\s+ALWAYS\s+AS\s+IDENTITY`))
	if err != nil {
		return err
	}
	if len(migrationHits) == 0 {
		return fmt.Errorf("no migration declares `chain_seq BIGINT GENERATED ALWAYS AS IDENTITY` — Decision #30 derives the chain tip from this column")
	}
	repoPath := filepath.Join(root, "services", "audit", "internal", "repository", "repository.go")
	body, err := readFile(repoPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", repoPath, err)
	}
	if !strings.Contains(body, "ORDER BY chain_seq DESC") {
		return fmt.Errorf("%s missing `ORDER BY chain_seq DESC` tip query — Decision #30 requires deriving the tip from audit_events itself", repoPath)
	}
	return nil
}

func ruleIsGlobalAdminColumn(root string) error {
	migrations := filepath.Join("services", "auth", "migrations")
	re := regexp.MustCompile(`is_global_admin`)
	hits, err := grepGlob(root, migrations, "*.sql", re)
	if err != nil {
		return err
	}
	if len(hits) == 0 {
		return fmt.Errorf("no migration under %s mentions is_global_admin — Decision #26 promises this typed primitive", migrations)
	}
	return nil
}

func ruleBootstrapTenantMetadata(root string) error {
	migrations := filepath.Join("services", "tenant", "migrations")
	re := regexp.MustCompile(`(?i)deployment_metadata|bootstrap_tenant_id`)
	hits, err := grepGlob(root, migrations, "*.sql", re)
	if err != nil {
		return err
	}
	if len(hits) == 0 {
		return fmt.Errorf("no migration under %s mentions deployment_metadata or bootstrap_tenant_id — Decision #28 wires the bootstrap CLI through this surface", migrations)
	}
	return nil
}

func ruleKeyRingSizeCap(root string) error {
	path := filepath.Join(root, "services", "auth", "internal", "service", "keyring.go")
	body, err := readFile(path)
	if err != nil {
		return fmt.Errorf("expected services/auth/internal/service/keyring.go (Phase 6.5): %w", err)
	}
	if !strings.Contains(body, "maxKeyRingSize") {
		return fmt.Errorf("%s missing maxKeyRingSize constant — CLAUDE.md §7 cites the SEC-048 ring cap as a load-bearing guard", path)
	}
	return nil
}

func ruleAESVersionConstant(root string) error {
	path := filepath.Join(root, "libs", "crypto", "aes", "aes.go")
	body, err := readFile(path)
	if err != nil {
		return fmt.Errorf("expected libs/crypto/aes/aes.go: %w", err)
	}
	// Phase 6.4 declares `const Version byte = 0x01`. Match the canonical
	// shape — the `byte` type is load-bearing (Encrypt prepends a single
	// byte, not a wider word), so insist on it.
	re := regexp.MustCompile(`\bVersion\s+byte\s*=\s*0x01\b`)
	if !re.MatchString(body) {
		return fmt.Errorf("%s missing `Version byte = 0x01` constant — Decision #29 promises a single-byte ciphertext version prefix", path)
	}
	return nil
}

func ruleNoTenantDomainsTable(root string) error {
	// Migrations are append-only, so the original CREATE TABLE survives in
	// history. RM-001 verification is "a DROP migration neutralised it" —
	// we assert a later DROP migration referencing tenant_domains exists.
	migrations := filepath.Join("services", "tenant", "migrations")
	dropRE := regexp.MustCompile(`(?i)DROP\s+TABLE[^;]*tenant_domains`)
	hits, err := grepGlob(root, migrations, "*.sql", dropRE)
	if err != nil {
		return err
	}
	if len(hits) == 0 {
		return fmt.Errorf("no DROP TABLE tenant_domains migration found under %s — CLAUDE.md §1 + RM-001 require this table be retired", migrations)
	}
	return nil
}

func ruleEventCatalogueCovered(root string) error {
	// The catalogue is the set of Routing* constants declared in
	// libs/rabbitmq/events. Every constant is either:
	//   a) referenced in services/audit/internal/eventconsumer/consumer.go's
	//      mapEvent switch (the consumer emits an AuditEvent row for it), OR
	//   b) annotated with a `// audit: skip` line in events.go (this routing
	//      key intentionally produces no audit row).
	eventsPath := filepath.Join(root, "libs", "rabbitmq", "events", "events.go")
	body, err := readFile(eventsPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", eventsPath, err)
	}
	// `Routing<Name> = "..."` — names are CamelCase, values are the actual
	// routing strings the consumer switches on.
	constRE := regexp.MustCompile(`(?m)^\s*(Routing\w+)\s*=\s*"([^"]+)"`)
	matches := constRE.FindAllStringSubmatchIndex(body, -1)
	if len(matches) == 0 {
		return fmt.Errorf("no Routing* constants discovered under %s — rule needs an update for the new layout", eventsPath)
	}
	skipRE := regexp.MustCompile(`//\s*audit:\s*skip`)
	type entry struct{ name, val string }
	var declared []entry
	for _, m := range matches {
		// m[0]..m[1] = full match; m[2]..m[3] = name; m[4]..m[5] = value.
		name := body[m[2]:m[3]]
		val := body[m[4]:m[5]]
		// Check the line containing the constant for the skip annotation.
		// Scan back to the previous newline so we capture inline comments
		// at the end of the const line OR a leading `// audit: skip` line.
		lineStart := strings.LastIndex(body[:m[0]], "\n") + 1
		lineEnd := m[1]
		// Pick up the trailing inline comment by extending to next newline.
		if next := strings.Index(body[lineEnd:], "\n"); next >= 0 {
			lineEnd += next
		}
		line := body[lineStart:lineEnd]
		// Also consider the immediately-preceding comment line as eligible
		// for the annotation, matching how Go const blocks are documented.
		precStart := strings.LastIndex(body[:lineStart-1], "\n") + 1
		preceding := body[precStart : lineStart-1]
		skipped := skipRE.MatchString(line) || skipRE.MatchString(preceding)
		if skipped {
			continue
		}
		declared = append(declared, entry{name: name, val: val})
	}
	consumerPath := filepath.Join(root, "services", "audit", "internal", "eventconsumer", "consumer.go")
	consumer, err := readFile(consumerPath)
	if err != nil {
		return fmt.Errorf("read consumer for catalogue check: %w", err)
	}
	var missing []string
	for _, e := range declared {
		// A consumer case can reference the routing key either by the
		// typed constant (`events.RoutingPushCompleted`) or by the raw
		// string literal in a defensive fallback — either is enough to
		// prove the catalogue closes the loop.
		if !strings.Contains(consumer, "events."+e.name) &&
			!strings.Contains(consumer, e.name) &&
			!strings.Contains(consumer, "\""+e.val+"\"") {
			missing = append(missing, e.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("Routing constants %v are declared in libs/rabbitmq/events but not switched on in %s and not annotated `// audit: skip` — Phase 6.3 closes this loop", missing, consumerPath)
	}
	return nil
}

func ruleEveryServiceValidatesMTLS(root string) error {
	// Each service that runs a gRPC server lives at services/<name>/cmd/server/main.go.
	// We assert every such main.go either invokes loader.ValidateMTLSConfig
	// or one of the BaseConfig methods that funnel through the same gate
	// (the Phase 1.3 wiring uses a config.Validate() that runs the mTLS
	// check inside).
	servicesDir := filepath.Join(root, "services")
	entries, err := os.ReadDir(servicesDir)
	if err != nil {
		return fmt.Errorf("read services dir: %w", err)
	}
	gateRE := regexp.MustCompile(`ValidateMTLSConfig|cfg\.Validate\(\)|loader\.Validate`)
	var missing []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mainPath := filepath.Join(servicesDir, e.Name(), "cmd", "server", "main.go")
		body, rerr := readFile(mainPath)
		if rerr != nil {
			if errors.Is(rerr, fs.ErrNotExist) {
				continue
			}
			return rerr
		}
		if !gateRE.MatchString(body) {
			missing = append(missing, mainPath)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("the following service main.go files don't call ValidateMTLSConfig / Validate (Phase 1.3 invariant): %v", missing)
	}
	return nil
}

func ruleMetricsExist(root string) error {
	path := filepath.Join(root, "libs", "observability", "metrics", "metrics.go")
	body, err := readFile(path)
	if err != nil {
		return fmt.Errorf("read metrics.go: %w", err)
	}
	// Each metric promised in CLAUDE.md §10. Names match the actual
	// Prometheus instrument names (the `Name:` field).
	required := []string{
		"registry_http_request_duration_seconds",
		"registry_grpc_request_duration_seconds",
		"registry_rabbitmq_messages_consumed_total",
		"registry_storage_operation_duration_seconds",
		"registry_active_uploads_total",
		"registry_grpc_peer_cn_denied_total",
		"registry_grpc_peer_cn_allowlist_enabled",
		"registry_auth_jwt_kid_fallback_total",
	}
	var missing []string
	for _, name := range required {
		if !strings.Contains(body, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("metrics declared in CLAUDE.md §10 but not registered in %s: %v", path, missing)
	}
	return nil
}

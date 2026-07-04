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
	"sort"
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
			Description: "CLAUDE.md §7 — every service main.go must call loader.ValidateMTLSConfig before starting servers OR carry a `// spec-lint: skip mtls-validate` annotation with a reason",
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
//
// CAVEAT: a missing file silently passes through as `false, nil`. If a rule
// REQUIRES the file to exist, the rule must perform its own presence check
// (readFile or os.Stat) BEFORE calling grepFile — otherwise a future repo
// reshuffle that deletes the file will be invisible to this lint.
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
	// SEC-053: the skip annotation MUST cite a reason after an em-dash or
	// ASCII hyphen (`// audit: skip — <reason>`), mirroring the mtls-validate
	// skip precedent in ruleEveryServiceValidatesMTLS. A bare `// audit: skip`
	// is rejected so a bad-faith PR cannot silently exempt a sensitive event by
	// dropping the annotation in the same diff that adds the constant — the
	// reason clause forces the author to state (and a reviewer to see) WHY the
	// event produces no audit row. The reason text is also surfaced in the
	// skip-count log below so the exemption list is visible in CI output.
	skipRE := regexp.MustCompile(`//\s*audit:\s*skip\s*[—-]\s*\S`)
	// A bare skip with no reason clause — matched separately so we can emit a
	// precise "cite a reason" error rather than the generic "not covered" one.
	bareSkipRE := regexp.MustCompile(`//\s*audit:\s*skip(\s*$|\s*[^—\-\s])`)
	type entry struct{ name, val string }
	var declared []entry
	// skipped collects the routing keys that carry a valid reason-bearing skip
	// annotation, so the count (and the constant names) land in the CI log —
	// reviewers see the exemption list drift over time (SEC-053 remediation #3).
	var skipped []string
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
		// Guard against the first-line case where there's no preceding line
		// (code-review-agent flagged the underflow: `body[:lineStart-1]`
		// would panic when lineStart == 0).
		var preceding string
		if lineStart > 0 {
			precStart := strings.LastIndex(body[:lineStart-1], "\n") + 1
			preceding = body[precStart : lineStart-1]
		}
		if skipRE.MatchString(line) || skipRE.MatchString(preceding) {
			// Valid skip: reason clause present. Record it for the count log
			// and do NOT require a consumer case.
			skipped = append(skipped, name)
			continue
		}
		if bareSkipRE.MatchString(line) || bareSkipRE.MatchString(preceding) {
			// A skip annotation is present but lacks the mandatory reason
			// clause — reject with a targeted message (SEC-053).
			return fmt.Errorf("%s: `// audit: skip` on %s lacks a reason clause — write `// audit: skip — <why this event produces no audit row>` (SEC-053)", eventsPath, name)
		}
		declared = append(declared, entry{name: name, val: val})
	}
	// Surface the exemption list in CI output so reviewers can watch it drift
	// (SEC-053 remediation #3). Sorted for a stable, diff-friendly line.
	if len(skipped) > 0 {
		sort.Strings(skipped)
		fmt.Printf("spec-lint: %d event(s) annotated `// audit: skip`: %v\n", len(skipped), skipped)
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
	//
	// Escape hatch: a service that is legitimately NOT part of the gRPC
	// mesh (e.g. services/mcp, which is a stdio/HTTP MCP server acting
	// as a CLIENT of the BFF) may declare `// spec-lint: skip mtls-validate`
	// in its main.go. Mirrors the `// audit: skip` precedent honoured by
	// ruleEventCatalogueCovered above. The annotation MUST be followed by
	// a `— <reason>` clause so a future reader understands the exemption.
	servicesDir := filepath.Join(root, "services")
	entries, err := os.ReadDir(servicesDir)
	if err != nil {
		return fmt.Errorf("read services dir: %w", err)
	}
	// SEC-054: match ONLY an explicit reference to the mTLS validator, not a
	// generic `cfg.Validate()` / `loader.Validate` that could check unrelated
	// fields. A service shipping its own `cfg.Validate()` (e.g. asserting
	// DB_DSN != "") would otherwise satisfy this rule without ever running the
	// mTLS path-validation gate CLAUDE.md §7 requires — letting a production
	// service silently fall back to insecure.NewCredentials(). Every current
	// gRPC service calls loader.ValidateMTLSConfig directly, so the tightened
	// regex is a no-op for today's tree and a real gate for future services.
	gateRE := regexp.MustCompile(`\bValidateMTLSConfig\b|\bmtls\.Validate\b`)
	// Skip annotation. em-dash (—) OR ASCII hyphen accepted so the rule
	// is friendly to environments where operators can't easily type U+2014.
	skipRE := regexp.MustCompile(`//\s*spec-lint:\s*skip\s+mtls-validate\s*[—-]\s*\S`)
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
		if gateRE.MatchString(body) {
			continue
		}
		if skipRE.MatchString(body) {
			continue
		}
		missing = append(missing, mainPath)
	}
	if len(missing) > 0 {
		return fmt.Errorf("the following service main.go files don't call ValidateMTLSConfig / Validate (Phase 1.3 invariant) and lack a `// spec-lint: skip mtls-validate — <reason>` annotation: %v", missing)
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

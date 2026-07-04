// Tests for the spec-lint rules hardened under SEC-053 (audit-skip must cite a
// reason) and SEC-054 (mTLS gate matches only the real validator). Each rule
// takes a repo root, so the tests build a minimal fixture tree in t.TempDir()
// and assert pass/fail on the exact edge cases the findings describe.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFixture writes body to <root>/<rel>, creating parent dirs.
func writeFixture(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", full, err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

// eventsCatalogueFixture lays down a minimal events.go + consumer.go pair under
// root. eventsBody is the events.go content; consumerBody is the consumer.go.
func eventsCatalogueFixture(t *testing.T, eventsBody, consumerBody string) string {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, root, "libs/rabbitmq/events/events.go", eventsBody)
	writeFixture(t, root, "services/audit/internal/eventconsumer/consumer.go", consumerBody)
	return root
}

// --- SEC-053: audit-skip must cite a reason -------------------------------

func TestEventCatalogue_SkipRequiresReason(t *testing.T) {
	// A bare `// audit: skip` with no reason clause must be REJECTED.
	events := `package events
const (
	RoutingThing = "thing.happened" // audit: skip
)`
	consumer := `package eventconsumer
func mapEvent() {}`
	root := eventsCatalogueFixture(t, events, consumer)

	err := ruleEventCatalogueCovered(root)
	if err == nil {
		t.Fatal("bare `// audit: skip` (no reason) must fail the rule")
	}
	if !strings.Contains(err.Error(), "reason clause") {
		t.Errorf("want a reason-clause error, got %q", err.Error())
	}
}

func TestEventCatalogue_SkipWithReasonPasses(t *testing.T) {
	// A reason-bearing skip is accepted even though the consumer has no case.
	events := `package events
const (
	RoutingThing = "thing.happened" // audit: skip — internal heartbeat, not an actor event
)`
	consumer := `package eventconsumer
func mapEvent() {}`
	root := eventsCatalogueFixture(t, events, consumer)

	if err := ruleEventCatalogueCovered(root); err != nil {
		t.Fatalf("reason-bearing skip should pass, got %v", err)
	}
}

func TestEventCatalogue_SkipWithAsciiHyphenReasonPasses(t *testing.T) {
	// An ASCII hyphen is accepted as the reason separator (not only the em-dash)
	// so operators who can't type U+2014 are not blocked.
	events := `package events
const (
	RoutingThing = "thing.happened" // audit: skip - internal heartbeat
)`
	consumer := `package eventconsumer
func mapEvent() {}`
	root := eventsCatalogueFixture(t, events, consumer)

	if err := ruleEventCatalogueCovered(root); err != nil {
		t.Fatalf("ASCII-hyphen reason skip should pass, got %v", err)
	}
}

func TestEventCatalogue_CoveredConstantPasses(t *testing.T) {
	// A constant switched on in the consumer needs no annotation.
	events := `package events
const (
	RoutingThing = "thing.happened"
)`
	consumer := `package eventconsumer
func mapEvent() {
	_ = events.RoutingThing
}`
	root := eventsCatalogueFixture(t, events, consumer)

	if err := ruleEventCatalogueCovered(root); err != nil {
		t.Fatalf("covered constant should pass, got %v", err)
	}
}

func TestEventCatalogue_UncoveredUnskippedFails(t *testing.T) {
	// The baseline invariant still holds: a constant neither switched on nor
	// annotated fails the rule.
	events := `package events
const (
	RoutingThing = "thing.happened"
)`
	consumer := `package eventconsumer
func mapEvent() {}`
	root := eventsCatalogueFixture(t, events, consumer)

	if err := ruleEventCatalogueCovered(root); err == nil {
		t.Fatal("an uncovered, unskipped constant must fail the rule")
	}
}

// --- SEC-054: mTLS gate matches only the real validator -------------------

// mtlsFixture writes a single service main.go under root and returns root.
func mtlsFixture(t *testing.T, service, mainBody string) string {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, root, filepath.Join("services", service, "cmd/server/main.go"), mainBody)
	return root
}

func TestMTLSGate_ValidateMTLSConfigPasses(t *testing.T) {
	root := mtlsFixture(t, "core", `package main
func main() {
	if err := loader.ValidateMTLSConfig(mtlsCfg); err != nil { panic(err) }
}`)
	if err := ruleEveryServiceValidatesMTLS(root); err != nil {
		t.Fatalf("explicit ValidateMTLSConfig should pass, got %v", err)
	}
}

func TestMTLSGate_GenericValidateRejected(t *testing.T) {
	// SEC-054: a generic cfg.Validate() that never touches the mTLS path must
	// NOT satisfy the rule.
	root := mtlsFixture(t, "core", `package main
func main() {
	if err := cfg.Validate(); err != nil { panic(err) }
}`)
	if err := ruleEveryServiceValidatesMTLS(root); err == nil {
		t.Fatal("generic cfg.Validate() must no longer satisfy the mTLS gate (SEC-054)")
	}
}

func TestMTLSGate_SkipAnnotationWithReasonPasses(t *testing.T) {
	root := mtlsFixture(t, "mcp", `package main
// spec-lint: skip mtls-validate — MCP is a stdio client, not part of the gRPC mesh
func main() {}`)
	if err := ruleEveryServiceValidatesMTLS(root); err != nil {
		t.Fatalf("reason-bearing skip annotation should pass, got %v", err)
	}
}

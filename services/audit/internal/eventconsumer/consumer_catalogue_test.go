//go:build !integration

package eventconsumer

// TestAuditCatalogueCompleteness enforces the §A5 invariant: every routing
// key defined in libs/rabbitmq/events MUST either appear as a case in this
// package's mapEvent switch OR carry a `// audit: skip` annotation in its
// definition. Without this guard, new event types silently drop on the
// floor.
//
// The test parses libs/rabbitmq/events/events.go for `Routing*` constants
// and their leading `// audit: skip` comments (if any), then reads this
// consumer.go for `case events.Routing<NAME>:` lines. Failures list every
// unmapped + unskipped key by name.

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestAuditCatalogueCompleteness is the §A5 lint-style test.
func TestAuditCatalogueCompleteness(t *testing.T) {
	t.Helper()

	// Resolve absolute paths relative to THIS file so the test works
	// regardless of where `go test` is invoked from.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed — cannot resolve file path")
	}
	// thisFile: .../services/audit/internal/eventconsumer/consumer_catalogue_test.go
	// events.go: .../libs/rabbitmq/events/events.go
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..")
	eventsPath := filepath.Join(repoRoot, "libs", "rabbitmq", "events", "events.go")
	consumerPath := filepath.Join(repoRoot, "services", "audit", "internal", "eventconsumer", "consumer.go")

	// --- Step 1: parse events.go to enumerate Routing* constants and their
	// "audit: skip" annotations. ---

	fset := token.NewFileSet()
	eventsAST, err := parser.ParseFile(fset, eventsPath, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("failed to parse events.go: %v", err)
	}

	// routingConsts maps constant name → "skip" (bool) and the comment text.
	type constInfo struct {
		name string
		skip bool
	}
	var routingConsts []constInfo

	// Walk the AST looking for const declarations with Routing* identifiers.
	// We need to check the comment group that immediately precedes each spec
	// in the GenDecl's Specs list, which ast stores as ValueSpec.Comment
	// (inline) or we can check the preceding comment blocks.
	//
	// Strategy: build a map from line number → comment text so we can look
	// up the comment on the line directly preceding a const declaration.
	lineComments := map[int]string{} // line → last comment text ending on that line

	for _, cg := range eventsAST.Comments {
		for _, c := range cg.List {
			line := fset.Position(c.Pos()).Line
			text := strings.TrimLeft(c.Text, "/ \t")
			lineComments[line] = text
		}
	}

	// Walk const declarations.
	for _, decl := range eventsAST.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok.String() != "const" {
			continue
		}
		for _, spec := range genDecl.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, ident := range vs.Names {
				if !strings.HasPrefix(ident.Name, "Routing") {
					continue
				}
				// Check for an "audit: skip" annotation. We look at the
				// comment on the line directly before the constant's line,
				// and also the ValueSpec's associated doc comment.
				constLine := fset.Position(ident.Pos()).Line
				skip := false

				// Check the line immediately preceding the spec.
				if prev, ok := lineComments[constLine-1]; ok {
					if strings.Contains(prev, "audit: skip") {
						skip = true
					}
				}
				// Check the inline comment on the same line.
				if c, ok := lineComments[constLine]; ok {
					if strings.Contains(c, "audit: skip") {
						skip = true
					}
				}
				// Check the ValueSpec's doc comment group.
				if vs.Doc != nil {
					for _, c := range vs.Doc.List {
						if strings.Contains(c.Text, "audit: skip") {
							skip = true
						}
					}
				}
				// Check the GenDecl's doc comment group (applies to all
				// specs in the declaration when there's only one spec per
				// line, e.g. const ( // audit: skip\n Routing... = "...").
				if !skip && genDecl.Doc != nil {
					for _, c := range genDecl.Doc.List {
						if strings.Contains(c.Text, "audit: skip") {
							skip = true
						}
					}
				}

				routingConsts = append(routingConsts, constInfo{
					name: ident.Name,
					skip: skip,
				})
			}
		}
	}

	if len(routingConsts) == 0 {
		t.Fatal("no Routing* constants found in events.go — check the file path")
	}

	// --- Step 2: scan consumer.go for `case events.Routing<NAME>:` lines. ---

	f, err := os.Open(consumerPath)
	if err != nil {
		t.Fatalf("failed to open consumer.go: %v", err)
	}
	defer f.Close()

	mappedCases := map[string]bool{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Match lines like `case events.RoutingFoo:` or `case events.RoutingFoo, events.RoutingBar:`
		if !strings.HasPrefix(line, "case events.Routing") {
			continue
		}
		// Strip "case " prefix and trailing ":"
		body := strings.TrimPrefix(line, "case ")
		body = strings.TrimSuffix(body, ":")
		// Split on commas for multi-constant cases
		parts := strings.Split(body, ",")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			// Remove "events." prefix
			if after, ok := strings.CutPrefix(part, "events."); ok {
				mappedCases[after] = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading consumer.go: %v", err)
	}

	// --- Step 3: assert every Routing* const is either mapped or skipped. ---

	var failures []string
	for _, c := range routingConsts {
		if c.skip {
			continue
		}
		if mappedCases[c.name] {
			continue
		}
		failures = append(failures, c.name)
	}

	if len(failures) > 0 {
		t.Errorf("§A5 audit catalogue gap: the following Routing* constants in "+
			"libs/rabbitmq/events/events.go have NO case in mapEvent and NO "+
			"`// audit: skip` annotation. Add a mapEvent case OR annotate the "+
			"constant with `// audit: skip — <reason>`:\n  %s",
			strings.Join(failures, "\n  "))
	}
}

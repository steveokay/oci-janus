// Command openapi-gen generates an OpenAPI 3.0 description of the
// registry-management REST (BFF) API directly from the route table.
//
// Why generate from the route table rather than hand-write a spec, or scatter
// swaggo annotations across 140+ handlers: the `mux.Handle("METHOD /path", …)`
// registrations in internal/handler are the single source of truth for the API
// surface. Parsing them means the paths, methods, path parameters, and auth
// requirement in the spec can never drift from the code — a CI drift-guard
// (make openapi + git diff --exit-status) enforces it. Request/response body
// schemas are intentionally generic here; enriching individual operations is a
// follow-up (see docs/api-reference.md).
//
// Usage:
//
//	go run ./cmd/openapi-gen            # writes ../../docs/openapi.json
//	go run ./cmd/openapi-gen -o out.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// route is one parsed (METHOD, path, secured) registration.
type route struct {
	Method  string
	Path    string
	Secured bool
}

// routeLit matches the "METHOD /path" string literal that every mux.Handle
// registration starts with.
var routeLit = regexp.MustCompile(`^(GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS) (/\S*)$`)

func main() {
	out := flag.String("o", filepath.Join("..", "..", "docs", "openapi.json"), "output path for the generated spec")
	dir := flag.String("dir", filepath.Join("internal", "handler"), "handler package directory to scan")
	flag.Parse()

	routes, err := parseRoutes(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "openapi-gen:", err)
		os.Exit(1)
	}
	if len(routes) == 0 {
		fmt.Fprintln(os.Stderr, "openapi-gen: no routes found — did the handler layout change?")
		os.Exit(1)
	}

	spec := buildSpec(routes)
	blob, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "openapi-gen:", err)
		os.Exit(1)
	}
	blob = append(blob, '\n') // trailing newline so the file is diff-clean
	if err := os.WriteFile(*out, blob, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "openapi-gen:", err)
		os.Exit(1)
	}
	fmt.Printf("openapi-gen: wrote %d routes to %s\n", len(routes), *out)
}

// parseRoutes walks every non-test .go file in dir and extracts each
// mux.Handle / mux.HandleFunc registration whose first argument is a
// "METHOD /path" string literal. A registration is "secured" when its handler
// argument mentions an *MW auth wrapper (authMW); the three public endpoints
// (/healthz, /api/v1/deployment-info, /webhooks/scm/github/pr) are not wrapped.
func parseRoutes(dir string) ([]route, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	seen := map[string]route{} // key: "METHOD path" — dedupe if registered twice
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if perr != nil {
			return nil, fmt.Errorf("parse %s: %w", name, perr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 1 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			m := routeLit.FindStringSubmatch(strings.Trim(lit.Value, "`\""))
			if m == nil {
				return true
			}
			r := route{Method: m[1], Path: m[2], Secured: mentionsAuthWrapper(call.Args[1:])}
			seen[r.Method+" "+r.Path] = r
			return true
		})
	}
	routes := make([]route, 0, len(seen))
	for _, r := range seen {
		routes = append(routes, r)
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		return routes[i].Method < routes[j].Method
	})
	return routes, nil
}

// mentionsAuthWrapper reports whether any identifier ending in "MW" (the
// auth-middleware naming convention) appears in the handler argument.
func mentionsAuthWrapper(args []ast.Expr) bool {
	found := false
	for _, a := range args {
		ast.Inspect(a, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && strings.HasSuffix(id.Name, "MW") {
				found = true
				return false
			}
			return true
		})
	}
	return found
}

var pathParam = regexp.MustCompile(`\{([^{}.]+)\.{0,3}\}`)

// buildSpec turns the parsed routes into an OpenAPI 3.0.3 document.
func buildSpec(routes []route) map[string]any {
	paths := map[string]any{}
	tagSet := map[string]bool{}

	for _, r := range routes {
		specPath, params := convertPath(r.Path)
		tag := tagFor(r.Path)
		tagSet[tag] = true

		op := map[string]any{
			"tags":        []string{tag},
			"summary":     fmt.Sprintf("%s %s", r.Method, r.Path),
			"operationId": operationID(r.Method, r.Path),
			"responses": map[string]any{
				"200": map[string]any{"description": "Success"},
				"400": ref("#/components/responses/Error"),
				"401": ref("#/components/responses/Error"),
				"403": ref("#/components/responses/Error"),
				"404": ref("#/components/responses/Error"),
			},
		}
		if len(params) > 0 {
			op["parameters"] = params
		}
		if r.Secured {
			op["security"] = []any{map[string]any{"BearerAuth": []any{}}}
		}

		item, _ := paths[specPath].(map[string]any)
		if item == nil {
			item = map[string]any{}
		}
		item[strings.ToLower(r.Method)] = op
		paths[specPath] = item
	}

	tags := make([]any, 0, len(tagSet))
	for _, name := range sortedKeys(tagSet) {
		tags = append(tags, map[string]any{"name": name})
	}

	return map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":       "OCI-Janus Management API",
			"version":     "1.0.0",
			"description": infoDescription,
		},
		"servers": []any{
			map[string]any{"url": "/", "description": "Same origin as the gateway"},
		},
		"tags":  tags,
		"paths": paths,
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"BearerAuth": map[string]any{
					"type":        "http",
					"scheme":      "bearer",
					"description": "RS256 JWT, or an API key of the form `key.<uuid>.<64-hex-secret>`.",
				},
			},
			"schemas": map[string]any{
				"Error": map[string]any{
					"type":       "object",
					"properties": map[string]any{"error": map[string]any{"type": "string"}},
				},
			},
			"responses": map[string]any{
				"Error": map[string]any{
					"description": "Error",
					"content": map[string]any{
						"application/json": map[string]any{
							"schema": ref("#/components/schemas/Error"),
						},
					},
				},
			},
		},
	}
}

// convertPath rewrites Go 1.22 mux "{name}"/"{name...}" segments into OpenAPI
// "{name}" segments and returns the corresponding required path parameters.
func convertPath(p string) (string, []any) {
	var params []any
	specPath := pathParam.ReplaceAllStringFunc(p, func(seg string) string {
		name := pathParam.FindStringSubmatch(seg)[1]
		params = append(params, map[string]any{
			"name":     name,
			"in":       "path",
			"required": true,
			"schema":   map[string]any{"type": "string"},
		})
		return "{" + name + "}"
	})
	return specPath, params
}

// tagFor groups an operation by the first meaningful path segment.
func tagFor(p string) string {
	segs := strings.Split(strings.Trim(p, "/"), "/")
	// /api/v1/<tag>/... → <tag>; otherwise the first segment (healthz, webhooks).
	if len(segs) >= 3 && segs[0] == "api" && segs[1] == "v1" {
		return segs[2]
	}
	if len(segs) > 0 && segs[0] != "" {
		return segs[0]
	}
	return "default"
}

func operationID(method, p string) string {
	s := strings.NewReplacer("/", "_", "{", "", "}", "", "...", "", "-", "_").Replace(strings.Trim(p, "/"))
	return strings.ToLower(method) + "_" + s
}

func ref(s string) map[string]any { return map[string]any{"$ref": s} }

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

const infoDescription = "REST API for the OCI-Janus registry management BFF (registry-management), " +
	"generated from the service route table.\n\n" +
	"**Authentication.** Send an `Authorization: Bearer <token>` header — either an RS256 JWT " +
	"(browser/dashboard) or an API key `key.<uuid>.<64-hex-secret>` (CI/automation). " +
	"Operations marked with a lock require authentication; API-key principals carry no roles, " +
	"so admin-only operations return 403.\n\n" +
	"**Conventions.** Pagination is cursor-based (`page_token` + `per_page`); errors return " +
	"`{\"error\": \"...\"}`. This document describes paths, methods, path parameters, and the " +
	"auth requirement exactly; request/query and response body schemas are being enriched " +
	"incrementally. The identity/session API (login, SSO, MFA, API keys) is served separately " +
	"by registry-auth and is not included here."

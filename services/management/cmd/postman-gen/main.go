// Command postman-gen converts the generated OpenAPI spec (docs/openapi.json)
// into a Postman v2.1 collection (docs/postman/registry-management.postman_collection.json).
//
// Generating the collection from the spec — which is itself generated from the
// route table — means the Postman collection covers every endpoint and never
// drifts. It is regenerated alongside the spec by `make openapi` and checked by
// the same CI drift-guard.
//
// Usage (run from the services/management module directory):
//
//	go run ./cmd/postman-gen
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// A fixed collection id keeps the output deterministic (Postman normally mints a
// random one). It only needs to be stable, not unique across workspaces.
const collectionID = "0c1a2b3c-0000-4000-8000-000000000001"

var pathVarRe = regexp.MustCompile(`\{([^{}]+)\}`)

// operation is one flattened (method, path) entry from the spec.
type operation struct {
	Method  string
	Path    string
	Tag     string
	Summary string
	Secured bool
	HasBody bool
	IsWrite bool
	// Body/Test are only set for curated routes (see curatedIdentityOps): a
	// ready-to-send JSON body and a Postman test script. Spec-derived routes
	// leave these empty and get a blank JSON stub instead.
	Body string
	Test []string
}

func main() {
	specPath := flag.String("spec", "../../docs/openapi.json", "path to the generated OpenAPI spec")
	out := flag.String("o", "../../docs/postman/registry-management.postman_collection.json", "output collection path")
	flag.Parse()

	blob, err := os.ReadFile(*specPath)
	if err != nil {
		fail(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(blob, &doc); err != nil {
		fail(err)
	}

	coll := buildCollection(doc)
	res, err := json.MarshalIndent(coll, "", "  ")
	if err != nil {
		fail(err)
	}
	res = append(res, '\n')
	if err := os.WriteFile(*out, res, 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("postman-gen: wrote %s\n", *out)
}

// curatedIdentityOps are the core registry-auth HTTP routes. They live in
// registry-auth (not the management BFF), so they are not in openapi.json, but a
// Postman user needs them — above all to log in and obtain a token. Kept small
// and stable on purpose; paths verified against services/auth/internal/handler.
var curatedIdentityOps = []operation{
	{
		Method: "POST", Path: "/api/v1/login", Tag: "identity", IsWrite: true,
		Summary: "Log in with a username + password → returns a JWT (or an MFA challenge). On success the test script captures the JWT into {{token}} for every other request.",
		Body:    "{\n  \"tenant_id\": \"{{tenantID}}\",\n  \"username\": \"{{username}}\",\n  \"password\": \"{{password}}\"\n}",
		Test: []string{
			"const body = pm.response.json();",
			"if (body.token) {",
			"  pm.collectionVariables.set('token', body.token);",
			"  console.log('captured token into {{token}}');",
			"} else if (body.mfa_required) {",
			"  pm.collectionVariables.set('challenge_token', body.challenge_token);",
			"  console.log('MFA required — challenge_token captured; run POST /api/v1/login/mfa');",
			"}",
		},
	},
	{Method: "POST", Path: "/api/v1/login/mfa", Tag: "identity", Summary: "Complete a TOTP MFA challenge (challenge_token + OTP) → returns a JWT", IsWrite: true},
	{Method: "POST", Path: "/api/v1/token/refresh", Tag: "identity", Summary: "Exchange the current Bearer token for a fresh one (token sent in the Authorization header, not the body)", Secured: true, IsWrite: true},
	{Method: "POST", Path: "/api/v1/logout", Tag: "identity", Summary: "Revoke the current session", Secured: true, IsWrite: true},
	{Method: "GET", Path: "/api/v1/auth/providers", Tag: "identity", Summary: "List enabled SSO providers"},
	{Method: "GET", Path: "/api/v1/users/me", Tag: "identity", Summary: "Current user profile", Secured: true},
	{Method: "GET", Path: "/api/v1/apikeys", Tag: "identity", Summary: "List your API keys", Secured: true},
	{Method: "POST", Path: "/api/v1/apikeys", Tag: "identity", Summary: "Issue an API key (shown once)", Secured: true, IsWrite: true},
	{Method: "DELETE", Path: "/api/v1/apikeys/{id}", Tag: "identity", Summary: "Revoke an API key", Secured: true},
}

func buildCollection(doc map[string]any) map[string]any {
	paths, _ := doc["paths"].(map[string]any)

	ops := append([]operation(nil), curatedIdentityOps...)
	pathVars := map[string]bool{}
	for p, pi := range paths {
		item, _ := pi.(map[string]any)
		for method, o := range item {
			om, _ := o.(map[string]any)
			m := strings.ToUpper(method)
			ops = append(ops, operation{
				Method:  m,
				Path:    p,
				Tag:     firstTag(om),
				Summary: str(om["summary"]),
				Secured: has(om, "security"),
				HasBody: has(om, "requestBody"),
				IsWrite: m == "POST" || m == "PUT" || m == "PATCH",
			})
		}
		for _, m := range pathVarRe.FindAllStringSubmatch(p, -1) {
			pathVars[m[1]] = true
		}
	}

	// Deterministic order: by tag, then path, then method.
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].Tag != ops[j].Tag {
			return ops[i].Tag < ops[j].Tag
		}
		if ops[i].Path != ops[j].Path {
			return ops[i].Path < ops[j].Path
		}
		return ops[i].Method < ops[j].Method
	})

	// Group into one folder per tag, preserving the sorted order.
	var folders []any
	var curTag string
	var cur []any
	flush := func() {
		if curTag != "" {
			folders = append(folders, map[string]any{"name": curTag, "item": cur})
		}
	}
	for _, op := range ops {
		if op.Tag != curTag {
			flush()
			curTag = op.Tag
			cur = nil
		}
		cur = append(cur, requestItem(op))
	}
	flush()

	return map[string]any{
		"info": map[string]any{
			"_postman_id": collectionID,
			"name":        "registry-management API",
			"description": "REST API for the OCI-Janus management BFF. Generated from docs/openapi.json " +
				"(itself generated from the service route table) — do not hand-edit; run `make openapi`. " +
				"The `identity` folder adds the core registry-auth routes (log in, refresh, API keys) so you " +
				"can obtain a token first. Set {{baseUrl}} and {{token}} (an RS256 JWT or a " +
				"`key.<uuid>.<secret>` API key) in an environment.",
			"schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json",
		},
		"auth": map[string]any{
			"type":   "bearer",
			"bearer": []any{map[string]any{"key": "token", "value": "{{token}}", "type": "string"}},
		},
		"variable": variables(pathVars),
		"item":     folders,
	}
}

// requestItem builds one Postman request from an operation.
func requestItem(op operation) map[string]any {
	segments := []string{}
	var urlVars []any
	for seg := range strings.SplitSeq(strings.TrimPrefix(op.Path, "/"), "/") {
		if m := pathVarRe.FindStringSubmatch(seg); m != nil {
			segments = append(segments, ":"+m[1])
			urlVars = append(urlVars, map[string]any{"key": m[1], "value": "{{" + m[1] + "}}"})
		} else {
			segments = append(segments, seg)
		}
	}

	url := map[string]any{
		"raw":  "{{baseUrl}}/" + strings.Join(segments, "/"),
		"host": []any{"{{baseUrl}}"},
		"path": toAny(segments),
	}
	if len(urlVars) > 0 {
		url["variable"] = urlVars
	}

	req := map[string]any{
		"method": op.Method,
		"header": []any{},
		"url":    url,
	}
	if op.Summary != "" {
		req["description"] = op.Summary
	}
	// Public endpoints override the collection-level bearer auth.
	if !op.Secured {
		req["auth"] = map[string]any{"type": "noauth"}
	}
	// A JSON body for write methods so the request is ready to send. Curated
	// routes may supply a filled body; everything else gets a blank stub.
	if op.IsWrite {
		raw := "{\n  \n}"
		if op.Body != "" {
			raw = op.Body
		}
		req["body"] = map[string]any{
			"mode": "raw",
			"raw":  raw,
			"options": map[string]any{
				"raw": map[string]any{"language": "json"},
			},
		}
	}

	item := map[string]any{"name": op.Method + " " + op.Path, "request": req}
	// A curated test script (e.g. the login token-capture) runs after the response.
	if len(op.Test) > 0 {
		item["event"] = []any{map[string]any{
			"listen": "test",
			"script": map[string]any{"type": "text/javascript", "exec": toAny(op.Test)},
		}}
	}
	return item
}

// variables emits the collection variables: baseUrl + token first, then every
// path parameter the spec uses (with a friendly default where obvious).
func variables(pathVars map[string]bool) []any {
	defaults := map[string]string{
		"org": "library", "repo": "app", "tag": "latest",
		"digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		// tenantID is the single-mode bootstrap tenant used by the dev stack; the
		// value is already public in .env.example / docker-compose.
		"tenantID": "98dbe36b-ef28-4903-b25c-bff1b2921c9e",
	}
	vars := []any{
		map[string]any{"key": "baseUrl", "value": "http://localhost:8085"},
		map[string]any{"key": "token", "value": ""},
		// Consumed by the identity/login request body; password is left blank so
		// no secret ships in the collection — fill it in your environment.
		map[string]any{"key": "username", "value": "admin"},
		map[string]any{"key": "password", "value": ""},
	}
	names := make([]string, 0, len(pathVars))
	for n := range pathVars {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		vars = append(vars, map[string]any{"key": n, "value": defaults[n]})
	}
	return vars
}

func firstTag(op map[string]any) string {
	if ts, ok := op["tags"].([]any); ok && len(ts) > 0 {
		if s, ok := ts[0].(string); ok {
			return s
		}
	}
	return "default"
}

func has(m map[string]any, key string) bool { _, ok := m[key]; return ok }

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "postman-gen:", err)
	os.Exit(1)
}

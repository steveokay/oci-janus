# FUT-031 SDK Reference — Official `modelcontextprotocol/go-sdk` v1.4.0

> Synthesised from the SDK source at `github.com\modelcontextprotocol\go-sdk@v1.4.0\mcp\`. This is the OFFICIAL Go SDK from the Model Context Protocol org — **use this, not mark3labs**. The subagent's original plan pointed at mark3labs which is not cached; the official SDK IS cached and is the better choice anyway.

## Module + import

```go
// go.mod
require github.com/modelcontextprotocol/go-sdk v1.4.0
require github.com/google/jsonschema-go v0.x.x  // transitive; pull via go mod tidy

// import
import "github.com/modelcontextprotocol/go-sdk/mcp"
```

## Constructing the server

```go
server := mcp.NewServer(
    &mcp.Implementation{Name: "oci-janus-registry", Version: "1.0.0"},
    nil, // *mcp.ServerOptions — nil is fine for defaults
)
```

## Registering tools — the TYPED form (recommended)

`mcp.AddTool` is a generic helper that infers the input JSON schema from your struct + wraps the handler:

```go
type ListReposInput struct {
    Org   string `json:"org,omitempty"   jsonschema:"filter by organisation name (optional)"`
    Limit int    `json:"limit,omitempty" jsonschema:"max rows to return (default 50, max 200)"`
}

type ListReposOutput struct {
    Repositories []Repository `json:"repositories" jsonschema:"the matched repositories"`
}

func listRepositoriesHandler(ctx context.Context, req *mcp.CallToolRequest, in ListReposInput) (*mcp.CallToolResult, ListReposOutput, error) {
    repos, err := client.ListRepositories(ctx, in.Org)
    if err != nil {
        return nil, ListReposOutput{}, err
    }
    return nil, ListReposOutput{Repositories: repos}, nil
    // Returning nil for *CallToolResult means the SDK builds the text
    // content from the typed output automatically.
}

// Register:
mcp.AddTool(server, &mcp.Tool{
    Name:        "registry_list_repositories",
    Description: "List the registry's repositories, optionally filtered by organisation. Use this to answer 'what repos do we have?' or 'what's in the prod org?'.",
}, listRepositoriesHandler)
```

## Registering tools — the RAW form (use when you want a hand-crafted JSON schema OR when the input is dynamic)

```go
server.AddTool(&mcp.Tool{
    Name:        "registry_list_audit_events",
    Description: "Search recent audit events. Supports action prefix + actor + resource filters. Capped at 500 rows.",
    InputSchema: json.RawMessage(`{
        "type": "object",
        "properties": {
            "action_prefix": {"type": "string", "description": "e.g. 'auth.', 'image.'"},
            "actor_id": {"type": "string"},
            "resource": {"type": "string"},
            "since_iso": {"type": "string", "description": "RFC3339 timestamp"},
            "limit": {"type": "integer", "minimum": 1, "maximum": 500, "default": 100}
        }
    }`),
}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
    var args struct {
        ActionPrefix string `json:"action_prefix"`
        ActorID      string `json:"actor_id"`
        Resource     string `json:"resource"`
        SinceISO     string `json:"since_iso"`
        Limit        int    `json:"limit"`
    }
    if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
        return nil, fmt.Errorf("invalid arguments: %w", err)
    }
    if args.Limit <= 0 || args.Limit > 500 {
        args.Limit = 100
    }
    events, err := c.ListAuditEvents(ctx, ...)
    if err != nil {
        // Error strings surface to the LLM — keep them action-oriented + never leak the API key.
        return nil, fmt.Errorf("query failed: %s (status %d)", err.Error(), 500)
    }
    body, _ := json.MarshalIndent(events, "", "  ")
    return &mcp.CallToolResult{
        Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
    }, nil
})
```

## Transports

### Stdio (Claude Desktop / Cursor stdio mode)

```go
// server.Run blocks until the transport disconnects.
// StdioTransport reads from os.Stdin, writes to os.Stdout.
// You MUST NOT write anything else to stdout (any stray print breaks the protocol).
if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
    slog.Error("mcp stdio server exited", "err", err)
    os.Exit(1)
}
```

### HTTP (Cursor remote / continue.dev / any HTTP-first client)

```go
// getServer is called PER incoming HTTP request. You can return the same
// long-lived *mcp.Server every time (fine for a single-tenant MCP surface
// like ours) or return a per-session copy.
handler := mcp.NewStreamableHTTPHandler(
    func(r *http.Request) *mcp.Server { return server },
    &mcp.StreamableHTTPOptions{
        // DisableLocalhostProtection: false — the default IS strict.
        // Only set true if you know what you're doing.
    },
)

httpSrv := &http.Server{
    Addr:              cfg.HTTPAddr,
    Handler:           handler,
    ReadHeaderTimeout: 10 * time.Second,
}
if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
    slog.Error("mcp http server exited", "err", err)
    os.Exit(1)
}
```

## `mcp.CallToolResult` + `mcp.Content`

- `mcp.CallToolResult{Content: []mcp.Content{...}}` — every tool returns text or resource content.
- `mcp.TextContent{Text: "..."}` implements `mcp.Content` — for JSON blobs, wrap in a text content with `\n\n`-separated sections + a fenced code block:

```go
body, _ := json.MarshalIndent(payload, "", "  ")
result := &mcp.CallToolResult{
    Content: []mcp.Content{
        &mcp.TextContent{Text: fmt.Sprintf("Found %d repositories.\n\n```json\n%s\n```", len(repos), body)},
    },
}
```

The LLM reads the text content and can either quote it verbatim or paraphrase.

## Load-bearing test invariants

1. **API key never leaks.** Test that runs every tool + grepping the captured slog handler's buffer + tool responses for the key prefix `key.` — assert 0 matches.

2. **Stdout is only MCP JSON-RPC frames in stdio mode.** Test by running the binary with a synthetic stdio pipe; capture stdout; unmarshal each newline-delimited line as JSON-RPC 2.0 — any parse failure = test failure. Log to stderr must have content; stdout must ONLY have `{"jsonrpc":"2.0",...}` lines.

3. **`registry_list_audit_events` caps `limit` at 500.** Pass `limit=99999` in args → the outbound HTTP call to the BFF uses `?limit=500`.

4. **All tools are read-only.** Inject a fake `http.Client` that records every method. Call every registered tool with dummy args → assert `Method` was `GET` (or `HEAD`) on every recorded call. No POST/PUT/DELETE/PATCH.

## Slog logging in stdio mode

Log to stderr, NEVER stdout:

```go
logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
```

(The SDK also exposes `mcp.NewLoggingHandler(session, nil)` for pushing logs to the CLIENT via MCP protocol, but that's ONLY useful once you have an active session — not for boot-time logs.)

## Package files worth reading if you need more depth

- `mcp/server.go` — `Server`, `NewServer`, `Run`, `Connect`, `AddTool`, `AddPrompt`, `AddResource`.
- `mcp/tool.go` — `Tool` struct, `AddTool` typed helper.
- `mcp/transport.go` — `StdioTransport`, `IOTransport`, `InMemoryTransport`.
- `mcp/streamable.go` — `StreamableHTTPHandler`, `NewStreamableHTTPHandler`, `StreamableHTTPOptions`.
- `mcp/content.go` — `Content` interface, `TextContent`.
- `mcp/tool_example_test.go` — the weather-tool example is the clearest read for how `AddTool` typed inference works.
- `mcp/server_example_test.go` — end-to-end prompts / resources examples.

## `go mod tidy` after adding

```bash
cd services/mcp
go mod tidy
# This will pull:
#   github.com/modelcontextprotocol/go-sdk v1.4.0
#   github.com/google/jsonschema-go (transitive)
#   github.com/yosida95/uritemplate (transitive)
#   plus jsonrpc2 helpers
```

The module cache already has v1.4.0 — no network fetch required at build time.

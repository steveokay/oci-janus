// Load-bearing invariant test for the stdio transport: EVERY line
// written to stdout must be a valid JSON-RPC 2.0 frame. Log lines
// interleaved into stdout would break the Claude Desktop client with
// a JSON parse error.
//
// This runs as a Go test so `go test ./...` catches a regression before
// PR. We spawn the compiled binary as a subprocess with a synthetic
// tools/list request on stdin + assert every line of its stdout is
// JSON-parseable.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestStdioStdoutIsOnlyJSONRPC compiles the binary and drives a real
// stdio session. Every stdout line must be JSON-parseable — a stray
// slog message on stdout would fail this test.
//
// Skipped on windows/darwin CI runners without `go` on PATH by way of
// checking exec.LookPath("go") — the test itself invokes `go run`.
func TestStdioStdoutIsOnlyJSONRPC(t *testing.T) {
	// The safest invariant test: compile-and-run with a synthetic
	// stdin script + verify stdout. `go run` is portable enough for
	// linux + darwin + windows dev machines.
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH; cannot compile subprocess")
	}

	// Locate the module root by walking up until we find go.mod.
	// runtime.Caller gives us the path of THIS file at compile time.
	_, thisFile, _, _ := runtime.Caller(0)
	modDir := filepath.Dir(thisFile) // .../services/mcp/cmd/server
	// modDir → .../services/mcp/cmd/server; the199 module root is 2 up
	moduleRoot := filepath.Dir(filepath.Dir(modDir)) // .../services/mcp

	// Build the binary into a temp dir so `go run` doesn't cache-race
	// with parallel tests.
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "registry-mcp")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	buildCmd := exec.Command("go", "build", "-o", bin, "./cmd/server")
	buildCmd.Dir = moduleRoot
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, string(out))
	}

	// Craft the smallest valid JSON-RPC 2.0 initialize + tools/list
	// sequence. The MCP handshake is stateful — we must send
	// initialize BEFORE tools/list or the SDK errors.
	//
	// Frames are line-delimited JSON on stdin.
	initFrame := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0.0"}}}` + "\n"
	initNotif := `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"
	listFrame := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"
	stdinBody := initFrame + initNotif + listFrame

	// Env: the whole point is we don't need mTLS / DB / RabbitMQ. Just
	// the 5 MCP env vars. The API key is a shape-valid dummy — the
	// tools don't actually get called in this test (we only ask for
	// tools/list), so no BFF is needed.
	env := []string{
		"LOG_LEVEL=info",
		"LOG_FORMAT=json",
		"MCP_TRANSPORT=stdio",
		"MCP_MANAGEMENT_URL=http://localhost:0",
		"MCP_API_KEY=key.11111111-1111-1111-1111-111111111111.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"MCP_TENANT_ID=22222222-2222-2222-2222-222222222222",
	}

	cmd := exec.Command(bin)
	cmd.Env = env

	// Use a pipe for stdin so we can flush the frames then keep the
	// pipe open long enough for the server to respond. Closing stdin
	// immediately after writing (the strings.NewReader path) races the
	// server's read loop — it sees EOF before it has parsed the
	// initialize request.
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Write the frames.
	if _, err := io.WriteString(stdinPipe, stdinBody); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	// Give the server ~1s to respond BEFORE closing stdin. This is
	// crude but portable — the SDK doesn't expose a "flush and ack"
	// hook we could hook on.
	time.Sleep(1 * time.Second)
	_ = stdinPipe.Close()

	// Wait bounded — never let a hung subprocess block the test suite.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			// Non-zero exit is fine as long as we captured some
			// stdout to inspect. The MCP SDK returns an error when
			// stdin EOFs mid-session — that's expected here.
			t.Logf("subprocess exited with %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("subprocess did not exit within 5s of stdin close")
	}

	stdoutBytes := stdout.Bytes()
	stderrBytes := stderr.Bytes()

	// The load-bearing assertion: every non-empty stdout line is
	// JSON-parseable.
	sc := bufio.NewScanner(bytes.NewReader(stdoutBytes))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	lineNum := 0
	sawInitializeResp := false
	sawToolsList := false
	for sc.Scan() {
		lineNum++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Errorf("stdout line %d is not JSON: %q\nerr: %v", lineNum, line, err)
			continue
		}
		// Sanity-check it's a JSON-RPC 2.0 frame.
		if v["jsonrpc"] != "2.0" {
			t.Errorf("stdout line %d is JSON but not JSON-RPC 2.0: %v", lineNum, v)
		}
		if id, ok := v["id"].(float64); ok {
			if id == 1 {
				sawInitializeResp = true
			}
			if id == 2 {
				sawToolsList = true
				// Extra: assert tools/list result contains the 12
				// expected tool names.
				if result, ok := v["result"].(map[string]any); ok {
					if toolsAny, ok := result["tools"].([]any); ok {
						if len(toolsAny) != 12 {
							t.Errorf("tools/list returned %d tools, want 12", len(toolsAny))
						}
					} else {
						t.Errorf("tools/list result.tools not an array: %v", result)
					}
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Errorf("scan stdout: %v", err)
	}

	if !sawInitializeResp {
		t.Errorf("did not see initialize response; stdout=%s\nstderr=%s", stdoutBytes, stderrBytes)
	}
	if !sawToolsList {
		t.Errorf("did not see tools/list response; stdout=%s\nstderr=%s", stdoutBytes, stderrBytes)
	}

	// Sibling assertion: stderr MUST contain at least one line (the
	// startup log). Proves that logs went to stderr, not stdout.
	if stderrBytes == nil || len(stderrBytes) == 0 {
		t.Error("stderr was empty; startup log did not route there")
	}
}

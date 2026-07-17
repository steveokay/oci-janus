# Scanner Engine Decoupling (Phase 2: grype) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Move the Grype engine out of the `registry-scanner` image into a `grype-engine` sidecar, mirroring Phase 1 (trivy). Completes FUT-090's core; after this the scanner image bakes **no** engine binaries (only the 4 adapter shims).

**Architecture:** Reuse the Phase 1 `engine-server` wrapper (already has a grype arg template) via `--build-arg ENGINE=grype`. Grype's upstream image is **distroless** (no shell), so the shell `engine-entrypoint.sh` is retired and its DB pre-warm + a new `-health` self-probe move **into the wrapper binary** — applied to *both* trivy-engine and grype-engine for consistency.

**Tech Stack:** Go 1.25.11 stdlib, Docker multi-stage, compose, Helm, React/TS.

**Branch:** `feat/scanner-engine-decoupling-grype` (already created). Never commit to main. Shared working tree — subagents must NOT switch branches / stash / checkout.

**Grounding facts (verified live):**
- grype base `anchore/grype:v0.93.0`: distroless, `Entrypoint=[/grype]`, `User=65532`, no `/bin/sh`.
- grype binary `/grype`; version = `grype version` (multi-line, has a `Version: X` line); scan = `grype dir:<rootfs> -o json --quiet`.
- The `engine-server/Dockerfile` already has `FROM anchore/grype:v0.93.0 AS engine-grype` and selects the base by `ENGINE` build-arg. It currently hardcodes `ENV ENGINE_CMD=/usr/local/bin/trivy` and uses `ENTRYPOINT ["/engine-entrypoint.sh"]` — both change in Task 1.
- grype-adapter (`infra/scanner-plugins/grype-adapter/main.go`) mirrors trivy-adapter: flatten rootfs → `exec grype dir:<rootfs> -o json --quiet` → parse `grypeJSON`. Keeps `translateFindings`, `extractLayer`, `countBySeverity`, `writeError`, types.

---

## Task 1: Wrapper consolidation — in-binary warm, `-health`, per-engine args, drop shell entrypoint

**Files:**
- Modify: `infra/scanner-plugins/engine-server/main.go`
- Modify: `infra/scanner-plugins/engine-server/main_test.go`
- Modify: `infra/scanner-plugins/engine-server/Dockerfile`
- Delete: `infra/scanner-plugins/engine-server/engine-entrypoint.sh`

- [ ] **Step 1: Extend the `server` struct + `serverFromEnv` for per-engine args**

In `main.go`, add fields to `server`: `versionArgs []string` and `warmArgs []string`. In `serverFromEnv`, default `ENGINE_CMD` per engine when unset, and set the per-engine arg templates:

```go
	cmd := envOr("ENGINE_CMD", "")
	if name == "" {
		return nil, "", fmt.Errorf("ENGINE_NAME is required")
	}
	// ... work/addr ...
	s := &server{engineName: name, workDir: work}
	switch name {
	case "trivy":
		if cmd == "" {
			cmd = "/usr/local/bin/trivy"
		}
		s.scanArgs = func(rootfs string) []string {
			return []string{"rootfs", "--quiet", "--no-progress", "--format", "json", "--skip-db-update", rootfs}
		}
		s.versionArgs = []string{"--version"}
		s.warmArgs = []string{"image", "--download-db-only"}
	case "grype":
		if cmd == "" {
			cmd = "/grype"
		}
		s.scanArgs = func(rootfs string) []string {
			return []string{"dir:" + rootfs, "-o", "json", "--quiet"}
		}
		s.versionArgs = []string{"version"}
		s.warmArgs = []string{"db", "update"}
	default:
		return nil, "", fmt.Errorf("unsupported ENGINE_NAME %q (want trivy|grype)", name)
	}
	s.engineCmd = cmd
	return s, addr, nil
```
(Remove the old requirement that `ENGINE_CMD` be non-empty; it now defaults per engine. `ENGINE_NAME` stays required.)

- [ ] **Step 2: Make `engineVersion` engine-agnostic**

Replace `engineVersion` so it runs `versionArgs` and scans ALL lines for a `Version:` field (grype's `version` output is multi-line; trivy's `--version` is `Version: X`):

```go
func (s *server) engineVersion() (string, error) {
	out, err := exec.Command(s.engineCmd, s.versionArgs...).Output() //nolint:gosec
	if err != nil {
		return "unknown", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Version:") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "Version:"))
			if v != "" {
				return v, nil
			}
		}
	}
	return "unknown", nil
}
```

- [ ] **Step 3: In-binary DB pre-warm at startup**

Add a `warm()` method and call it (best-effort, synchronous, before serving) in `main()` unless `SCANNER_SKIP_ENGINE_WARM` is set. This replaces `engine-entrypoint.sh`:

```go
// warm runs the engine's one-time DB download so the first scan pays no
// network cost. Best-effort: a failure logs but never blocks serving — the
// first real scan self-heals (trivy falls back online for a cold cache; grype
// auto-updates). Skipped when SCANNER_SKIP_ENGINE_WARM is set.
func (s *server) warm() {
	if os.Getenv("SCANNER_SKIP_ENGINE_WARM") != "" {
		return
	}
	fmt.Fprintf(os.Stderr, "engine-server: pre-warming %s DB...\n", s.engineName)
	cmd := exec.Command(s.engineCmd, s.warmArgs...) //nolint:gosec
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "engine-server: %s DB warm failed (%v) — first scan will fetch online\n", s.engineName, err)
		return
	}
	fmt.Fprintf(os.Stderr, "engine-server: %s DB ready\n", s.engineName)
}
```
In `main()`, after `serverFromEnv` and before building the mux/ListenAndServe, call `s.warm()`.

- [ ] **Step 4: `-health` self-probe flag (distroless-safe healthcheck)**

At the very top of `main()`, before `serverFromEnv`, handle the health subcommand so the same binary can probe itself (grype's distroless image has no wget/curl):

```go
func main() {
	if len(os.Args) > 1 && os.Args[1] == "-health" {
		os.Exit(healthProbe())
	}
	// ... existing serverFromEnv + warm + serve ...
}

// healthProbe GETs the local /healthz and returns a process exit code. Used as
// the container healthcheck in distroless images that lack a shell/wget.
func healthProbe() int {
	addr := envOr("ENGINE_HTTP_ADDR", ":8085")
	// Normalize ":8085" -> "127.0.0.1:8085".
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		fmt.Fprintf(os.Stderr, "engine-server -health: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
```

- [ ] **Step 5: Update tests**

In `main_test.go`, update `newTestServer` to also set `versionArgs`/`warmArgs` (e.g. `versionArgs: []string{"--version"}`) so the existing `TestHealthz_OK` (which calls `engineVersion`) still works with the stub engine. The stub already answers `--version`. Add a small test that `serverFromEnv` defaults `ENGINE_CMD` for grype (`ENGINE_NAME=grype` → `engineCmd == "/grype"`, `versionArgs == ["version"]`). Keep the existing 4 tests green.

Run: `cd infra/scanner-plugins/engine-server && GOWORK=off go test ./... && GOWORK=off go vet ./... && gofmt -l .`
Expected: pass (Windows skips the exec-based scan tests; the new serverFromEnv test runs everywhere), vet+gofmt clean.

- [ ] **Step 6: Dockerfile — drop the shell entrypoint, set caches for both engines**

Edit `infra/scanner-plugins/engine-server/Dockerfile`:
- Remove `COPY infra/scanner-plugins/engine-server/engine-entrypoint.sh /engine-entrypoint.sh`.
- Remove `ENV ENGINE_CMD=/usr/local/bin/trivy` (wrapper defaults it per engine now).
- Replace `ENV TRIVY_CACHE_DIR=/engine-cache` with both caches (harmless to set both regardless of engine):
  ```dockerfile
  ENV TRIVY_CACHE_DIR=/engine-cache
  ENV GRYPE_DB_CACHE_DIR=/engine-cache
  ```
- Change the entrypoint to the wrapper binary directly (works on both alpine + distroless):
  ```dockerfile
  ENTRYPOINT ["/usr/local/bin/engine-server"]
  ```
  (Drop the `CMD ["/usr/local/bin/engine-server"]` line — the ENTRYPOINT is the binary now.)
- Keep `USER root` (grype base defaults to 65532; root ensures `/engine-cache` is writable and can read the scanner's 0700 rootfs on the shared volume — verified in Phase 1).

- [ ] **Step 7: Delete `engine-entrypoint.sh`**

`git rm infra/scanner-plugins/engine-server/engine-entrypoint.sh`

- [ ] **Step 8: Build BOTH engine images + smoke test**

From repo root:
```bash
docker build -f infra/scanner-plugins/engine-server/Dockerfile --build-arg ENGINE=trivy -t oci-janus/trivy-engine:dev .
docker build -f infra/scanner-plugins/engine-server/Dockerfile --build-arg ENGINE=grype -t oci-janus/grype-engine:dev .
```
Smoke both (note grype is distroless — use the `-health` binary, not wget):
```bash
docker run -d --name te-smoke -e SCANNER_SKIP_ENGINE_WARM=1 oci-janus/trivy-engine:dev && sleep 3 && docker exec te-smoke /usr/local/bin/engine-server -health && echo trivy-health-OK; docker rm -f te-smoke
docker run -d --name ge-smoke -e SCANNER_SKIP_ENGINE_WARM=1 oci-janus/grype-engine:dev && sleep 3 && docker exec ge-smoke /usr/local/bin/engine-server -health && echo grype-health-OK; docker rm -f ge-smoke
```
Expected: both print `...-health-OK` (exit 0). If grype-engine can't exec (distroless nuance), report it.

- [ ] **Step 9: Commit**
```bash
git add infra/scanner-plugins/engine-server/main.go infra/scanner-plugins/engine-server/main_test.go infra/scanner-plugins/engine-server/Dockerfile
git rm infra/scanner-plugins/engine-server/engine-entrypoint.sh
git commit -m "feat(scanner): in-binary engine warm + -health probe; distroless-safe entrypoint"
```

---

## Task 2: Repoint the grype-adapter to the sidecar

Mirror the Phase 1 trivy-adapter change exactly.

**Files:** `infra/scanner-plugins/grype-adapter/main.go` (+ `main_test.go`)

- [ ] **Step 1: Failing tests** — add to `main_test.go` (imports `encoding/json`, `net/http`, `net/http/httptest`, `strings`, `testing`):
```go
func TestScanViaEngine_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["rootfs"] == "" {
			t.Errorf("rootfs missing from POST")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"engine":"grype","version":"0.93.0","raw":{"matches":[{"vulnerability":{"id":"CVE-1","severity":"High"},"artifact":{"name":"p","version":"1"}}],"descriptor":{"name":"grype","version":"0.93.0"}}}`))
	}))
	defer srv.Close()
	report, version, err := scanViaEngine(srv.URL, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if version != "0.93.0" {
		t.Fatalf("want version 0.93.0, got %q", version)
	}
	fs := translateFindings(report)
	if len(fs) != 1 || fs[0].CVE != "CVE-1" || fs[0].Severity != "HIGH" {
		t.Fatalf("bad findings: %+v", fs)
	}
}

func TestScanViaEngine_Unreachable(t *testing.T) {
	_, _, err := scanViaEngine("http://127.0.0.1:1", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), errEngineUnreachable) {
		t.Fatalf("want %q error, got %v", errEngineUnreachable, err)
	}
}
```
Run RED: `cd infra/scanner-plugins/grype-adapter && GOWORK=off go test ./... -run TestScanViaEngine -v` → undefined `scanViaEngine`/`errEngineUnreachable`.

- [ ] **Step 2: Implement** — in `grype-adapter/main.go`:
  - Imports: ADD `net/http`, `time`, `bytes`. REMOVE `os/exec`.
  - DELETE `grypeBinary` (var), `runGrype`, `grypeVersion`, `pluginEnv`.
  - Add the sentinel + helpers (grype-flavored — note `GRYPE_ENGINE_URL`):
```go
const errEngineUnreachable = "engine_unreachable"

func engineURL() string { return os.Getenv("GRYPE_ENGINE_URL") }

func scanWorkDir() string {
	if d := os.Getenv("SCANNER_SCAN_WORK_DIR"); d != "" {
		return d
	}
	return "/scan-work"
}

// scanViaEngine POSTs the flattened rootfs path to the grype-engine sidecar and
// returns the parsed grype report + the engine's self-reported version. A
// connection/timeout failure is wrapped with errEngineUnreachable so the
// orchestrator classifies it as a deploy problem, not a scan failure.
func scanViaEngine(baseURL, rootfs string) (*grypeJSON, string, error) {
	if baseURL == "" {
		return nil, "unknown", fmt.Errorf("GRYPE_ENGINE_URL not set; cannot reach grype-engine sidecar")
	}
	body, _ := json.Marshal(map[string]string{"rootfs": rootfs})
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Post(strings.TrimRight(baseURL, "/")+"/scan", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, "unknown", fmt.Errorf("%s: POST /scan: %w", errEngineUnreachable, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode != http.StatusOK {
		var er struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &er)
		return nil, "unknown", fmt.Errorf("grype-engine returned %d: %s", resp.StatusCode, strings.TrimSpace(er.Error))
	}
	var env struct {
		Version string          `json:"version"`
		Raw     json.RawMessage `json:"raw"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, "unknown", fmt.Errorf("decode engine response: %w", err)
	}
	var report grypeJSON
	if err := json.Unmarshal(env.Raw, &report); err != nil {
		return nil, env.Version, fmt.Errorf("parse grype json: %w", err)
	}
	// Prefer the version embedded in the grype report when present.
	if report.Descriptor.Version != "" {
		return &report, report.Descriptor.Version, nil
	}
	return &report, env.Version, nil
}
```
  - In `main()`: replace `os.MkdirTemp("", "grype-rootfs-*")` with the shared-workdir form (MkdirAll(scanWorkDir()) then `os.MkdirTemp(scanWorkDir(), "grype-rootfs-*")`), keep the layer-staging loop, and replace `runGrype(rootfs)` with `scanViaEngine(engineURL(), rootfs)`:
```go
	if err := os.MkdirAll(scanWorkDir(), 0o755); err != nil {
		writeError(req.ID, fmt.Sprintf("mkdir work dir: %v", err))
		os.Exit(1)
	}
	rootfs, err := os.MkdirTemp(scanWorkDir(), "grype-rootfs-*")
	if err != nil {
		writeError(req.ID, fmt.Sprintf("mkdtemp rootfs: %v", err))
		os.Exit(1)
	}
	defer os.RemoveAll(rootfs)
	// ... existing layer loop ...
	report, scannerVersion, err := scanViaEngine(engineURL(), rootfs)
	if err != nil {
		writeError(req.ID, err.Error())
		os.Exit(1)
	}
```
  - Update the package doc: step 3 becomes "POST the flattened rootfs path to the grype-engine sidecar (`$GRYPE_ENGINE_URL/scan`)".

- [ ] **Step 3: GREEN + gates** — `cd infra/scanner-plugins/grype-adapter && GOWORK=off go test ./... -v && GOWORK=off go vet ./... && gofmt -l . && GOWORK=off go build ./...`. Remove any now-obsolete tests referencing deleted symbols (let the compiler name them).

- [ ] **Step 4: Commit** — `git add infra/scanner-plugins/grype-adapter/main.go infra/scanner-plugins/grype-adapter/main_test.go && git commit -m "feat(scanner): grype-adapter posts to grype-engine sidecar instead of exec"`

---

## Task 3: Scanner Dockerfile — drop the baked Grype engine

**File:** `services/scanner/Dockerfile` (+ `scripts/entrypoint.sh`)

- [ ] **Step 1:** Delete the `FROM anchore/grype:v0.93.0 AS grype` stage + comment; delete `COPY --from=grype /grype /usr/local/bin/grype`; delete `ENV GRYPE_DB_CACHE_DIR=/grype-cache` + its comment; remove `/grype-cache` from the `mkdir -p` + `chown`; change `VOLUME ["/grype-cache", "/scan-work"]` to `VOLUME ["/scan-work"]`. Keep the 4 adapter COPYs (grype-*adapter* stays; only the grype *engine binary* leaves). After this the scanner image bakes **no** engine binaries.
- [ ] **Step 2:** In `scripts/entrypoint.sh`, delete the Grype pre-warm block (`SCANNER_SKIP_GRYPE_WARM` / `/usr/local/bin/grype`). At this point the entrypoint only auto-fills the plugin checksum. Leave the checksum logic + `exec "$@"`.
- [ ] **Step 3:** Build: `docker build -f services/scanner/Dockerfile -t oci-janus/scanner:dev .`; verify NO trivy AND NO grype binary, but all 4 `scanner-*` adapters present:
  ```bash
  docker run --rm --entrypoint sh oci-janus/scanner:dev -c 'ls /usr/local/bin/scanner-*; for b in trivy grype; do test -x /usr/local/bin/$b && echo "$b PRESENT(bad)" || echo "$b absent(good)"; done'
  ```
- [ ] **Step 4:** Commit: `git add services/scanner/Dockerfile services/scanner/scripts/entrypoint.sh && git commit -m "refactor(scanner): drop baked Grype engine from scanner image"`

---

## Task 4: Compose — grype-engine service + scanner wiring

**File:** `infra/docker-compose/docker-compose.yml`

- [ ] **Step 1:** Add a `grype-engine` service mirroring `trivy-engine` (read the existing trivy-engine block and match it): same `build` (context `../..`, `dockerfile: infra/scanner-plugins/engine-server/Dockerfile`, `args: ENGINE: grype`), `image: oci-janus/grype-engine:dev`, profile `["scanner"]`, network `registry`, env `ENGINE_NAME: grype` + `ENGINE_HTTP_ADDR: ":8085"`, volumes `scan_work:/scan-work:ro` + `grype_engine_cache:/engine-cache`. Healthcheck uses the `-health` binary (grype is distroless — no wget):
  ```yaml
      healthcheck:
        test: ["CMD", "/usr/local/bin/engine-server", "-health"]
        interval: 10s
        timeout: 5s
        retries: 5
        start_period: 90s
  ```
  Also switch the **existing trivy-engine** healthcheck from the `wget` form to `["CMD", "/usr/local/bin/engine-server", "-health"]` + `start_period: 90s` (the shell entrypoint is gone; keep both consistent).
- [ ] **Step 2:** On `registry-scanner`, add env `GRYPE_ENGINE_URL: "http://grype-engine:8085"` and add `grype-engine: {condition: service_healthy}` to its `depends_on`.
- [ ] **Step 3:** Declare `grype_engine_cache:` in the top-level `volumes:`.
- [ ] **Step 4:** Validate: `cd infra/docker-compose && docker compose --profile scanner config >/dev/null && echo OK`.
- [ ] **Step 5:** Commit: `git add infra/docker-compose/docker-compose.yml && git commit -m "feat(scanner): compose wiring for grype-engine sidecar"`

---

## Task 5: Helm — grype-engine sidecar + health-probe map + CI + docs

**Files:** `infra/helm/registry/charts/scanner/{values.yaml,templates/deployment.yaml}`, `services/scanner/internal/handler/grpc.go`, `.github/workflows/ci-scanner-engines.yml`, `docs/SERVICES.md`, `futures.md`, `status.md`

- [ ] **Step 1 (Helm):** In `values.yaml` add a `grypeEngine` block mirroring `trivyEngine` (`enabled: true`, image `ghcr.io/steveokay/oci-janus/grype-engine` tag `0.93.0`, `httpPort: 8085`, resources). In `templates/deployment.yaml`, add a second sidecar container `grype-engine` (mirror the trivy-engine container: env `ENGINE_NAME=grype` + `ENGINE_HTTP_ADDR`, `livenessProbe.httpGet /healthz` on its port, volumeMounts `scan-work` ro + a new `grype-engine-cache` emptyDir at `/engine-cache`), gated on `.Values.grypeEngine.enabled`. Add `GRYPE_ENGINE_URL` to the scanner container env (gated). Add the `grype-engine-cache` emptyDir to pod volumes. NOTE both engine containers can share port 8085 only if in **separate** pods — since they're in the SAME pod here, give grype-engine a distinct port (e.g. `8086`) via its `httpPort` value and `ENGINE_HTTP_ADDR=":8086"` + `GRYPE_ENGINE_URL=http://localhost:8086`. Verify: `helm template infra/helm/registry --show-only charts/scanner/templates/deployment.yaml` shows THREE containers (scanner + trivy-engine + grype-engine) with distinct ports; `helm lint infra/helm/registry` clean.
- [ ] **Step 2 (health map):** In `services/scanner/internal/handler/grpc.go`, add `"grype-adapter": "GRYPE_ENGINE_URL"` to `adapterEngineURLEnv`. Run `cd services/scanner && GOWORK=off go test ./internal/handler/ -v` (the existing probe tests still pass; add one asserting an active `grype-adapter` with a dead `GRYPE_ENGINE_URL` reports unreachable=false).
- [ ] **Step 3 (CI):** In `.github/workflows/ci-scanner-engines.yml`, add a second image build step for `--build-arg ENGINE=grype` (mirror the trivy build step).
- [ ] **Step 4 (docs/trackers):** Update `docs/SERVICES.md` scanner note (grype now a sidecar too; scanner bakes no engine binaries). Flip `futures.md` FUT-090's grype item to shipped (leave cosign/registry-pull/runtime-install open). Prepend a `status.md` row for Phase 2.
- [ ] **Step 5:** Commit each logical group (helm; handler+test; ci; docs) or one combined commit: `git commit -m "feat(scanner): grype-engine helm + health probe + CI + docs (Phase 2)"`.

---

## Task 6: Live-verify + PR

- [ ] Rebuild + up `grype-engine` + `registry-scanner` (+ trivy-engine for the healthcheck change): `docker compose -f infra/docker-compose/docker-compose.yml --profile scanner build grype-engine trivy-engine registry-scanner && ... up -d grype-engine trivy-engine registry-scanner`. Confirm both engines healthy (via the new `-health` compose healthcheck).
- [ ] Activate the grype adapter (`PATCH /api/v1/admin/scanners/active {adapter_path:/usr/local/bin/scanner-grype-adapter}`), push a known-vulnerable image (older-but-not-EOSL alpine, e.g. **alpine:3.9**), confirm the scan runs through grype-engine: `scanner_name=grype`, real findings > 0. Confirm `active_adapter_engine_reachable:true` in BFF health.
- [ ] Stop `grype-engine` → scan `failed` with `engine_unreachable` in scanner logs + health `reachable:false`. Restart → recovers.
- [ ] Clean up demo repo. Push branch, open PR, merge (squash, delete branch), sync main, rebuild.

---

## Notes / out of scope (still FUT-090 tail)
Cosign-verify of engine images; A3 registry-pull; operator-installable engines at runtime. Clair already a sidecar. After Phase 2 the scanner image bakes zero engine binaries.

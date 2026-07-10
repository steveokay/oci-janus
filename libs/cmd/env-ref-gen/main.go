// Command env-ref-gen consolidates every service's .env.example file into a
// single Markdown environment-variable reference (docs/env-reference.md).
//
// The .env.example files are the authoritative, human-maintained documentation
// for each service's configuration (CLAUDE.md §2 per-service layout: "All env
// vars documented"). Rather than hand-maintain a second copy that drifts, this
// tool parses them — "# --- Section ---" headers, "# comment" lines above each
// "KEY=value" — and emits one deterministic reference page. A CI drift-guard
// (docs-env-ref.yml) regenerates it and fails if the committed page is stale,
// so the reference can never fall behind the .env.example files.
//
// Usage (run from the libs module directory):
//
//	go run ./cmd/env-ref-gen                 # ../services -> ../docs/env-reference.md
//	go run ./cmd/env-ref-gen -services X -o Y
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// envVar is one KEY=value entry with its parsed section + description.
type envVar struct {
	Key     string
	Default string
	Desc    string
	Section string
}

var (
	sectionRe = regexp.MustCompile(`^#\s*-{2,}\s*(.*?)\s*-{2,}\s*$`) // # --- Section ---
	keyRe     = regexp.MustCompile(`^([A-Z][A-Z0-9_]*)=(.*)$`)
	dividerRe = regexp.MustCompile(`^#\s*-{2,}\s*$`) // a bare "# ----" rule
)

func main() {
	servicesDir := flag.String("services", filepath.Join("..", "services"), "directory containing the service subdirectories")
	out := flag.String("o", filepath.Join("..", "docs", "env-reference.md"), "output Markdown path")
	flag.Parse()

	entries, err := os.ReadDir(*servicesDir)
	if err != nil {
		fail(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, statErr := os.Stat(filepath.Join(*servicesDir, e.Name(), ".env.example")); statErr == nil {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		fail(fmt.Errorf("no services/*/.env.example files found under %s", *servicesDir))
	}

	var b strings.Builder
	total := 0
	writeHeader(&b, len(names))
	for _, name := range names {
		vars, perr := parseEnvFile(filepath.Join(*servicesDir, name, ".env.example"))
		if perr != nil {
			fail(perr)
		}
		total += len(vars)
		writeService(&b, name, vars)
	}

	if err := os.WriteFile(*out, []byte(b.String()), 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("env-ref-gen: wrote %d variables across %d services to %s\n", total, len(names), *out)
}

// parseEnvFile reads a .env.example, attributing each KEY=value to the most
// recent "# --- Section ---" header and the run of "# comment" lines directly
// above it. A blank line resets the pending comment run so a variable never
// inherits an unrelated comment from further up the file.
func parseEnvFile(path string) ([]envVar, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var (
		vars     []envVar
		section  string
		comments []string
	)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		switch {
		case strings.TrimSpace(line) == "":
			comments = comments[:0]
		case sectionRe.MatchString(line):
			section = sectionRe.FindStringSubmatch(line)[1]
			comments = comments[:0]
		case dividerRe.MatchString(line):
			// a decorative rule — ignore
		case strings.HasPrefix(line, "#"):
			comments = append(comments, strings.TrimSpace(strings.TrimPrefix(line, "#")))
		case keyRe.MatchString(line):
			m := keyRe.FindStringSubmatch(line)
			vars = append(vars, envVar{
				Key:     m[1],
				Default: m[2],
				Desc:    strings.Join(comments, " "),
				Section: section,
			})
			comments = comments[:0]
		}
	}
	return vars, sc.Err()
}

func writeHeader(b *strings.Builder, nServices int) {
	fmt.Fprintf(b, `# Environment variable reference

Every configuration variable for all %d services, consolidated from each
service's `+"`.env.example`"+` file. These files are the source of truth; this
page is **generated** from them (`+"`libs/cmd/env-ref-gen`"+`) and a CI
drift-guard keeps it in sync — so it never falls behind the code.

`, nServices)
	b.WriteString(`!!! warning "Secrets come from the environment"
    The **Example / default** column shows the placeholder from ` + "`.env.example`" + `.
    An **empty** value means the variable has no default and must be supplied
    (secrets always do). Never commit real secrets — inject them from a secrets
    manager. See [Self-hosting](SELF-HOSTING.md) for the KEK inventory and
    production guidance.

!!! note "Regenerating"
    ` + "```bash\n    cd libs && go run ./cmd/env-ref-gen   # writes ../docs/env-reference.md\n    ```" + `

`)
}

func writeService(b *strings.Builder, name string, vars []envVar) {
	fmt.Fprintf(b, "## registry-%s\n\n", name)
	fmt.Fprintf(b, "`services/%s/.env.example`\n\n", name)

	curSection := "\x00" // sentinel so the first real section always prints
	inTable := false
	for _, v := range vars {
		if v.Section != curSection {
			if inTable {
				b.WriteString("\n") // close the previous table before the next heading
			}
			curSection = v.Section
			heading := shortSection(curSection)
			if heading == "" {
				heading = "General"
			}
			fmt.Fprintf(b, "### %s\n\n", mdEscape(heading))
			b.WriteString("| Variable | Example / default | Description |\n|---|---|---|\n")
			inTable = true
		}
		def := v.Default
		defCell := "—"
		if def != "" {
			defCell = "`" + mdEscape(def) + "`"
		}
		desc := v.Desc
		if desc == "" {
			desc = "—"
		}
		fmt.Fprintf(b, "| `%s` | %s | %s |\n", v.Key, defCell, mdEscape(desc))
	}
	if inTable {
		b.WriteString("\n")
	}
}

// shortSection trims a verbose "# --- ... ---" header down to a concise
// heading — the part before the first explanatory delimiter (an em dash, a
// parenthetical, or a sentence break). "mTLS — production fail-safe (…). Set…"
// becomes "mTLS"; "Email notification channel (FUT-019 Phase 3)" becomes
// "Email notification channel". The full text still lives in the .env.example.
func shortSection(s string) string {
	for _, cut := range []string{" — ", " - ", " (", ". "} {
		if i := strings.Index(s, cut); i > 0 {
			s = s[:i]
		}
	}
	return strings.TrimSpace(s)
}

// mdEscape neutralises the two characters that would break a Markdown table
// cell or its layout: the column separator and stray newlines.
func mdEscape(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "env-ref-gen:", err)
	os.Exit(1)
}

import { describe, it, expect } from "vitest";
import { buildSnippets, SNIPPET_FORMATS } from "../credential-snippets";

// credential-snippets test — verifies the pure renderer substitutes the
// hostname + SA name into all four supported formats and refuses to leak
// shell-breaking characters.
describe("buildSnippets", () => {
  const hostname = "registry.example.com";
  const saName = "ci-prod";

  it("renders all four supported formats", () => {
    const snippets = buildSnippets({ hostname, saName });
    expect(Object.keys(snippets).sort()).toEqual([...SNIPPET_FORMATS].sort());
  });

  it("substitutes the hostname into the docker login snippet", () => {
    const { "docker login": s } = buildSnippets({ hostname, saName });
    expect(s).toContain("registry.example.com");
    expect(s).toContain("--username ci-prod");
    // No <REGISTRY_HOST> placeholder should leak through.
    expect(s).not.toContain("<REGISTRY_HOST>");
  });

  it("substitutes the hostname into the kubernetes Secret snippet", () => {
    const { "kubernetes Secret": s } = buildSnippets({ hostname, saName });
    expect(s).toContain("--docker-server=registry.example.com");
    expect(s).toContain("--docker-username=ci-prod");
    expect(s).not.toContain("<REGISTRY_HOST>");
  });

  it("substitutes the hostname into the terraform snippet", () => {
    const { terraform: s } = buildSnippets({ hostname, saName });
    expect(s).toContain('username = "ci-prod"');
    expect(s).toContain("registry.example.com");
  });

  it("substitutes the hostname into the GitHub Actions snippet", () => {
    const { "GitHub Actions": s } = buildSnippets({ hostname, saName });
    expect(s).toContain("registry: registry.example.com");
    expect(s).toContain("username: ci-prod");
  });

  it("escapes special characters in the service-account name", () => {
    const { "docker login": s } = buildSnippets({
      hostname,
      saName: 'evil"name',
    });
    // The renderer must not break shell-quoting — embedded double-quote
    // gets escaped or the SA name gets rejected at create-time. Today the
    // SA name regex disallows ", so this is defence in depth.
    expect(s).not.toContain('--username evil"name');
  });

  // SEC-055 — the FE sanitiser is now an allowlist mirroring the
  // server-side SA-name regex `[a-z0-9._-]`. This test pins the
  // allowlist contract so a future loosening of the regex on either
  // side can't silently widen the snippet's attack surface.
  it("strips any character outside the [a-z0-9._-] allowlist", () => {
    const cases: Array<[string, string]> = [
      ["semicolons", "evil;rm -rf"],
      ["pipes", "ci|exfil"],
      ["newlines", "line1\nline2"],
      ["spaces", "with space"],
      ["unicode", "ci-‮-drop"],
      ["uppercase", "MIXED-Case"],
    ];
    for (const [label, raw] of cases) {
      const { "docker login": s } = buildSnippets({ hostname, saName: raw });
      // None of the disallowed characters should appear in the rendered
      // --username slot. We match the literal "--username " prefix and
      // assert the substring up to the next backslash-newline is
      // [a-z0-9._-] only.
      const m = s.match(/--username (.*?) \\/);
      expect(m, `${label}: --username slot not found`).not.toBeNull();
      const rendered = m![1];
      expect(
        rendered,
        `${label}: ${JSON.stringify(rendered)} contains a disallowed char`
      ).toMatch(/^[a-z0-9._-]*$/);
    }
  });
});

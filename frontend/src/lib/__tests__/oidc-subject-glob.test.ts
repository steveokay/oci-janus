import { describe, test, expect } from "vitest";
import { validateGlobSyntax } from "../oidc-subject-glob";

// Tests for the client-side OIDC subject-pattern syntax validator (FUT-001
// Task 15). Mirrors the rejection rules in
// services/auth/internal/service/oidc_subject.go#validateGlobSyntax so the
// dialog surfaces the same error the BE would return, before the request
// even leaves the browser. Not a matcher — the BE is authoritative at
// exchange time.

describe("validateGlobSyntax", () => {
  // ── Rejection cases ────────────────────────────────────────────────────

  test("rejects the empty string", () => {
    const r = validateGlobSyntax("");
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.error).toMatch(/non-empty/i);
  });

  test("rejects whitespace-only strings", () => {
    const r = validateGlobSyntax("   ");
    expect(r.ok).toBe(false);
  });

  test("rejects three consecutive asterisks", () => {
    const r = validateGlobSyntax("repo:foo/***");
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.error).toMatch(/\*/);
  });

  test("rejects four consecutive asterisks anywhere in the pattern", () => {
    const r = validateGlobSyntax("prefix/****/suffix");
    expect(r.ok).toBe(false);
  });

  test("rejects leading whitespace", () => {
    const r = validateGlobSyntax(" repo:foo/bar");
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.error).toMatch(/whitespace/i);
  });

  test("rejects trailing whitespace", () => {
    const r = validateGlobSyntax("repo:foo/bar ");
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.error).toMatch(/whitespace/i);
  });

  // ── Acceptance cases ───────────────────────────────────────────────────

  test("accepts a single literal", () => {
    expect(validateGlobSyntax("repo:steveokay/oci-janus:ref:refs/heads/main"))
      .toEqual({ ok: true });
  });

  test("accepts a single asterisk", () => {
    expect(validateGlobSyntax("*")).toEqual({ ok: true });
  });

  test("accepts a doublestar", () => {
    expect(validateGlobSyntax("**")).toEqual({ ok: true });
  });

  test("accepts a doublestar in the middle of a segment", () => {
    expect(validateGlobSyntax("repo:steveokay/**:ref:refs/heads/*"))
      .toEqual({ ok: true });
  });

  test("accepts a single question mark", () => {
    expect(validateGlobSyntax("?")).toEqual({ ok: true });
  });

  test("accepts `*/` (single-star followed by literal slash)", () => {
    expect(validateGlobSyntax("*/main")).toEqual({ ok: true });
  });

  test("accepts a mix of ?, *, and **", () => {
    expect(validateGlobSyntax("proj/?/branch/**/*")).toEqual({ ok: true });
  });
});

// oidc-subject-glob — client-side syntax validator for OIDC trust
// subject_pattern strings (FUT-001 Task 15).
//
// The BE is the authoritative matcher at exchange time
// (services/auth/internal/service/oidc_subject.go). This file only mirrors
// the *syntax* rules in `validateGlobSyntax` so a mis-typed pattern is
// rejected in the CreateOIDCTrustDialog without a round-trip.
//
// Rejection rules (must stay in lock-step with the Go version):
//   - Empty string is rejected — matches only the empty `sub` claim, which
//     is never legitimate.
//   - Runs of 3+ consecutive `*` are rejected (`**` is the well-defined
//     doublestar; a third star would be ambiguous).
//   - Leading or trailing whitespace is rejected — almost always a
//     copy-paste bug.
//
// NOTE: this is a SYNTAX check, not a matcher. It never tells the operator
// whether a given `sub` claim would match — that is deliberately BE-owned
// so a divergence between FE and BE globbing can't produce a false-positive
// "your pattern will let this in" UI.

export type GlobValidation = { ok: true } | { ok: false; error: string };

// validateGlobSyntax reports whether `pattern` is a syntactically valid
// subject glob per the rules above.
export function validateGlobSyntax(pattern: string): GlobValidation {
  if (pattern === "") {
    return { ok: false, error: "pattern must be non-empty" };
  }
  // Walk the pattern looking for runs of '*' longer than 2. Matches the Go
  // implementation's offset-in-error-message style so operators can find
  // the trouble spot in long patterns.
  for (let i = 0; i < pattern.length; ) {
    if (pattern[i] !== "*") {
      i++;
      continue;
    }
    let runLen = 0;
    for (let j = i; j < pattern.length && pattern[j] === "*"; j++) {
      runLen++;
    }
    if (runLen > 2) {
      return {
        ok: false,
        error: `pattern contains a run of ${runLen} consecutive '*' at offset ${i} (max 2 for \`**\`)`,
      };
    }
    i += runLen;
  }
  // Trim comparison catches all leading/trailing whitespace variants
  // (spaces, tabs, newlines) with a single call.
  if (pattern !== pattern.trim()) {
    return {
      ok: false,
      error: "pattern must not have leading or trailing whitespace",
    };
  }
  return { ok: true };
}

// Package rekey CLI-layer unit tests — exercise the RunCLI flag/validation
// plumbing with no database. Anything that reaches pgxpool is out of scope
// here (covered by the integration-tagged sweep_test.go); these pin the
// pre-DB validation gates so a flag regression is caught cheaply.
package rekey

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// Two distinct valid 32-byte (64 hex char) keys, derived at runtime from the
// same deterministic key32 helper used in rekey_test.go. Computing them (rather
// than embedding a 64-hex literal) keeps the secret scanner from flagging a
// dummy test key as a generic-api-key finding.
var (
	keyHexA = hex.EncodeToString(key32(0xA1))
	keyHexB = hex.EncodeToString(key32(0xB2))
)

// runCLIArgs invokes RunCLI with a discarded stdout and the given args, after
// setting (or clearing) the KEK env + DSN env for the case. dsnEnv defaults to
// a name that is guaranteed unset so a case that gets past key validation trips
// the "DSN required" gate rather than dialing a real database.
func runCLIArgs(t *testing.T, args []string, oldHex, newHex string) error {
	t.Helper()
	// t.Setenv restores + forbids parallel, so these never leak across cases.
	if oldHex == "" {
		t.Setenv("KEK_OLD_HEX", "")
	} else {
		t.Setenv("KEK_OLD_HEX", oldHex)
	}
	if newHex == "" {
		t.Setenv("KEK_NEW_HEX", "")
	} else {
		t.Setenv("KEK_NEW_HEX", newHex)
	}
	return RunCLI(context.Background(), args, "ROTATE_KEK_TEST_DSN_UNSET", nil, io.Discard)
}

func isValidationErr(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}

// TestSelectSQL_LockClause pins the SEC-071 concurrency posture directly (the
// Docker-gated sweep_test.go exercises the query but never asserts on the lock
// clause): rotate/dry-run must write-lock candidate rows with FOR UPDATE, and
// the read-only verify pass must NOT. A regression that drops or flips the
// clause would otherwise slip through the standard (no-Docker) CI lane.
func TestSelectSQL_LockClause(t *testing.T) {
	spec := TableSpec{
		Table:    "widgets",
		PKColumn: "id",
		Columns:  []CipherColumn{{Name: "secret_enc"}},
	}
	if locked := selectSQL(spec, true); !strings.Contains(locked, "FOR UPDATE") {
		t.Errorf("rotate/dry-run selectSQL must append FOR UPDATE, got %q", locked)
	}
	if unlocked := selectSQL(spec, false); strings.Contains(unlocked, "FOR UPDATE") {
		t.Errorf("verify selectSQL must be a lock-free read, got %q", unlocked)
	}
}

// TestRunCLI_DryRunAndVerifyMutuallyExclusive — the two inspection modes cannot
// be combined.
func TestRunCLI_DryRunAndVerifyMutuallyExclusive(t *testing.T) {
	err := runCLIArgs(t, []string{"--dry-run", "--verify"}, keyHexA, keyHexB)
	if !isValidationErr(err) {
		t.Fatalf("want ValidationError, got %v", err)
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("want mutually-exclusive message, got %q", err.Error())
	}
}

// TestRunCLI_ToVersionBounds — --to-version must be in [1, 32767] so narrowing
// to the int16 kek_version column can neither wrap nor accept a nonsensical
// generation (code-review #3). 32767 is valid and must fall through to the key
// gates (a DIFFERENT error), proving the bounds check itself passed.
func TestRunCLI_ToVersionBounds(t *testing.T) {
	for _, bad := range []string{"0", "-1", "32768", "99999"} {
		err := runCLIArgs(t, []string{"--to-version", bad}, keyHexA, keyHexB)
		if !isValidationErr(err) || !strings.Contains(err.Error(), "to-version") {
			t.Errorf("--to-version %s: want to-version ValidationError, got %v", bad, err)
		}
	}
	// 32767 is in-bounds: the error (if any) must NOT be the bounds message.
	err := runCLIArgs(t, []string{"--to-version", "32767"}, keyHexA, keyHexB)
	if err != nil && strings.Contains(err.Error(), "to-version must be between") {
		t.Errorf("--to-version 32767 should be in-bounds, got bounds error: %v", err)
	}
}

// TestRunCLI_BadNewKey — an invalid KEK_NEW_HEX is a ValidationError before any
// DB work.
func TestRunCLI_BadNewKey(t *testing.T) {
	err := runCLIArgs(t, nil, keyHexA, "not-hex")
	if !isValidationErr(err) || !strings.Contains(err.Error(), "KEK_NEW_HEX") {
		t.Fatalf("want KEK_NEW_HEX ValidationError, got %v", err)
	}
}

// TestRunCLI_MissingNewKey — an empty KEK_NEW_HEX is rejected.
func TestRunCLI_MissingNewKey(t *testing.T) {
	err := runCLIArgs(t, nil, keyHexA, "")
	if !isValidationErr(err) || !strings.Contains(err.Error(), "KEK_NEW_HEX") {
		t.Fatalf("want KEK_NEW_HEX ValidationError, got %v", err)
	}
}

// TestRunCLI_EqualKeysRejected — SEC-073: OLD == NEW would rotate nothing yet
// report success, so it is rejected up front (rotate/dry-run paths only).
func TestRunCLI_EqualKeysRejected(t *testing.T) {
	err := runCLIArgs(t, nil, keyHexA, keyHexA)
	if !isValidationErr(err) || !strings.Contains(err.Error(), "identical") {
		t.Fatalf("want identical-keys ValidationError, got %v", err)
	}
}

// TestRunCLI_VerifyNeedsOnlyNewKey — --verify parses NEW alone (no OLD), so an
// unset KEK_OLD_HEX must NOT trip the equal-key or old-key gates; it falls
// through to the DSN-required gate instead.
func TestRunCLI_VerifyNeedsOnlyNewKey(t *testing.T) {
	err := runCLIArgs(t, []string{"--verify"}, "", keyHexB)
	if !isValidationErr(err) || !strings.Contains(err.Error(), "ROTATE_KEK_TEST_DSN_UNSET") {
		t.Fatalf("verify with only NEW key should reach the DSN gate, got %v", err)
	}
}

// TestRunCLI_MissingDSN — with valid distinct keys the rotate path reaches the
// DSN gate and reports the required env var by name (no DB dial attempted).
func TestRunCLI_MissingDSN(t *testing.T) {
	err := runCLIArgs(t, nil, keyHexA, keyHexB)
	if !isValidationErr(err) || !strings.Contains(err.Error(), "ROTATE_KEK_TEST_DSN_UNSET") {
		t.Fatalf("want DSN-required ValidationError, got %v", err)
	}
}

// TestRunCLI_GenerateWritesKeyToStdoutCaveatToStderr — --generate prints a
// single 64-hex key on stdout (pipeline-friendly) and the SEC-072 warning on
// stderr, then returns nil without needing keys or a DB.
func TestRunCLI_GenerateWritesKeyToStdoutCaveatToStderr(t *testing.T) {
	// Capture os.Stderr for the duration so we can assert the caveat routes
	// there and not to the stdout writer.
	origStderr := os.Stderr
	rp, wp, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = wp
	defer func() { os.Stderr = origStderr }()

	var stdout strings.Builder
	runErr := RunCLI(context.Background(), []string{"--generate"}, "ROTATE_KEK_TEST_DSN_UNSET", nil, &stdout)

	_ = wp.Close()
	os.Stderr = origStderr
	stderrBytes, _ := io.ReadAll(rp)

	if runErr != nil {
		t.Fatalf("--generate returned error: %v", runErr)
	}
	got := strings.TrimSpace(stdout.String())
	if len(got) != 64 {
		t.Errorf("stdout key: want 64 hex chars, got %d (%q)", len(got), got)
	}
	// The printed key must itself be a valid KEK.
	if _, perr := ParseKeyHex(got); perr != nil {
		t.Errorf("generated key does not parse as a valid KEK: %v", perr)
	}
	if !strings.Contains(string(stderrBytes), "WARNING") {
		t.Errorf("expected a WARNING caveat on stderr, got %q", string(stderrBytes))
	}
}

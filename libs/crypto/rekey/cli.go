package rekey

import (
	"context"
	"crypto/subtle"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

// maxKEKVersion is the largest value the kek_version SMALLINT column can hold.
// --to-version is validated against this so an int flag cannot silently wrap or
// truncate when narrowed to int16 (code-review #3).
const maxKEKVersion = 32767

// ValidationError signals operator-input problems (bad flags, missing/invalid
// keys). The service main.go dispatch maps it to exit code 2.
type ValidationError struct{ msg string }

func (e *ValidationError) Error() string { return e.msg }

func validationError(format string, a ...any) *ValidationError {
	return &ValidationError{msg: fmt.Sprintf(format, a...)}
}

// ErrRowsRemain is returned by RunCLI in --verify mode when at least one row is
// still on the old key. The dispatch maps it to exit code 3 for scripting.
var ErrRowsRemain = errors.New("rows still on the old key")

// RunCLI implements the shared `rotate-kek` subcommand body. Each service calls
// it with its own DSN environment-variable name and TableSpecs.
//
//	args      — os.Args[2:] (everything after "rotate-kek")
//	dsnEnv    — name of the env var holding the service DSN (e.g. "DB_DSN")
//	specs     — the service's table specs
//	stdout    — where human-readable output is written
//
// Modes (mutually exclusive flags; default is rotate):
//
//	--generate       mint + print a fresh 32-byte hex KEK, then exit (no DB)
//	--dry-run        report candidate counts without mutating
//	--verify         report rows still on the old key (exit 3 if any remain)
//	--to-version N   override the stamped generation (default: max+1)
//
// Keys come from the environment, never flags (avoids shell-history leakage):
//
//	KEK_OLD_HEX  required for rotate + dry-run
//	KEK_NEW_HEX  required for rotate + dry-run + verify
func RunCLI(ctx context.Context, args []string, dsnEnv string, specs []TableSpec, stdout io.Writer) error {
	fs := flag.NewFlagSet("rotate-kek", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dryRun := fs.Bool("dry-run", false, "report counts without mutating")
	verify := fs.Bool("verify", false, "report rows still on the old key")
	generate := fs.Bool("generate", false, "mint and print a fresh 32-byte hex KEK, then exit")
	toVersion := fs.Int("to-version", 0, "override the stamped kek_version (default: max+1)")
	if err := fs.Parse(args); err != nil {
		return validationError("parse flags: %v", err)
	}

	// Detect whether --to-version was explicitly provided. The flag default is 0,
	// which is also the "auto = max+1" sentinel; fs.Visit lets us tell an
	// explicit --to-version 0 (a bounds error) apart from an omitted flag.
	toVersionSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "to-version" {
			toVersionSet = true
		}
	})

	if *generate {
		h, err := GenerateKeyHex()
		if err != nil {
			return fmt.Errorf("generate key: %w", err)
		}
		// The key is the sole stdout line so `rotate-kek --generate` can be piped
		// into a secrets manager. The caveat goes to STDERR (SEC-072) so it does
		// not corrupt that pipe, while still warning an interactive operator.
		fmt.Fprintln(stdout, h)
		fmt.Fprintln(os.Stderr, "WARNING: this key is printed to stdout — avoid running under CI log capture; store it in your secrets manager immediately.")
		return nil
	}
	if *dryRun && *verify {
		return validationError("--dry-run and --verify are mutually exclusive")
	}

	// Validate --to-version bounds before touching the DB so the failure is a
	// cheap, DB-free ValidationError (code-review #3). An int flag narrowed to
	// the int16 kek_version column would otherwise wrap (>32767) or accept a
	// nonsensical zero/negative generation.
	if toVersionSet && (*toVersion <= 0 || *toVersion > maxKEKVersion) {
		return validationError("--to-version must be between 1 and %d, got %d", maxKEKVersion, *toVersion)
	}

	newKey, err := ParseKeyHex(os.Getenv("KEK_NEW_HEX"))
	if err != nil {
		return validationError("KEK_NEW_HEX: %v", err)
	}
	var oldKey []byte
	if !*verify {
		oldKey, err = ParseKeyHex(os.Getenv("KEK_OLD_HEX"))
		if err != nil {
			return validationError("KEK_OLD_HEX: %v", err)
		}
		// Reject a no-op rotation where OLD == NEW (SEC-073). Without this an
		// operator who fat-fingers the same value into both env vars gets a clean
		// "rotated N rows" success while nothing actually changed key. Only the
		// rotate/dry-run paths reach here — --verify uses NewKey alone and
		// --generate returned above — so both keys are guaranteed present.
		// Constant-time compare avoids leaking key equality via timing.
		if subtle.ConstantTimeCompare(oldKey, newKey) == 1 {
			return validationError("KEK_OLD_HEX and KEK_NEW_HEX are identical — rotation would be a no-op")
		}
	}

	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		return validationError("%s environment variable is required", dsnEnv)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect DB: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping DB: %w", err)
	}

	switch {
	case *verify:
		rep, err := Sweep(ctx, pool, specs, SweepOpts{Mode: ModeVerify, NewKey: newKey})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "verify: %d row(s) still on the old key\n", rep.RowsOnOldKey)
		for tbl, n := range rep.PerTable {
			fmt.Fprintf(stdout, "  %s: %d on old key\n", tbl, n)
		}
		if rep.RowsOnOldKey > 0 {
			return ErrRowsRemain
		}
		return nil

	default: // rotate or dry-run
		ver := int16(*toVersion)
		if ver == 0 {
			ver, err = NextVersion(ctx, pool, specs)
			if err != nil {
				return err
			}
		}
		mode := ModeRotate
		label := "rotated"
		if *dryRun {
			mode = ModeDryRun
			label = "would rotate"
		}
		rep, err := Sweep(ctx, pool, specs, SweepOpts{
			Mode: mode, OldKey: oldKey, NewKey: newKey, ToVersion: ver,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s %d row(s) to kek_version %d\n", label, rep.RowsRotated, ver)
		for tbl, n := range rep.PerTable {
			fmt.Fprintf(stdout, "  %s: %d\n", tbl, n)
		}
		return nil
	}
}

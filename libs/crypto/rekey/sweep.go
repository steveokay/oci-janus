package rekey

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Encoding describes how a cipher column stores its bytes.
type Encoding int

const (
	// EncodingBytea is a raw BYTEA column (the common case).
	EncodingBytea Encoding = iota
	// EncodingHexText is a TEXT column holding hex-encoded ciphertext
	// (webhook.secret_enc is the only such column in the platform).
	EncodingHexText
)

// CipherColumn identifies one encrypted column and how it is stored.
type CipherColumn struct {
	Name     string
	Encoding Encoding
}

// TableSpec declares everything the sweep needs to rotate one table: its name,
// primary-key column, the tracking column, and its cipher columns. A table may
// carry more than one cipher column (audit_export_configs has two); they all
// rotate together in the table's single transaction.
type TableSpec struct {
	Table         string
	PKColumn      string
	VersionColumn string
	Columns       []CipherColumn
	// Optional marks a table that may not exist in every deployment (the
	// legacy auth_providers table). When true and the table is absent, the
	// sweep logs a skip and moves on instead of erroring.
	Optional bool
}

// Mode selects the sweep behaviour.
type Mode int

const (
	// ModeRotate re-encrypts every candidate row and commits.
	ModeRotate Mode = iota
	// ModeDryRun performs every decrypt/encrypt but rolls back — it reports
	// how many rows would rotate without mutating anything.
	ModeDryRun
	// ModeVerify never mutates; it reports how many rows still fail to
	// decrypt under NewKey (i.e. remain on the old key). Only NewKey is used.
	ModeVerify
)

// SweepOpts carries the keys, mode, and target version for a sweep.
type SweepOpts struct {
	Mode      Mode
	OldKey    []byte // required for ModeRotate/ModeDryRun
	NewKey    []byte // required for all modes
	ToVersion int16  // stamped on rotated rows (ModeRotate)
}

// Report summarises a sweep across all tables.
type Report struct {
	RowsRotated  int            // rows re-encrypted (ModeRotate) or candidate rows (ModeDryRun)
	RowsOnOldKey int            // rows still on the old key (ModeVerify)
	PerTable     map[string]int // table name → row count touched/inspected
}

// Sweep runs the requested mode over every spec. Each table is processed in its
// own transaction (all-or-nothing): if any cell in a table fails to decrypt
// under OldKey, that table's transaction rolls back and Sweep returns the error
// with the offending primary key. Secrets are never logged — only counts and
// primary keys.
func Sweep(ctx context.Context, pool *pgxpool.Pool, specs []TableSpec, opts SweepOpts) (Report, error) {
	rep := Report{PerTable: map[string]int{}}
	for _, spec := range specs {
		if spec.Optional {
			exists, err := tableExists(ctx, pool, spec.Table)
			if err != nil {
				return rep, err
			}
			if !exists {
				slog.Info("rotate-kek: optional table absent, skipping", "table", spec.Table)
				continue
			}
		}
		if opts.Mode == ModeVerify {
			n, err := verifyTable(ctx, pool, spec, opts.NewKey)
			if err != nil {
				return rep, err
			}
			rep.RowsOnOldKey += n
			rep.PerTable[spec.Table] = n
			continue
		}
		n, err := rotateTable(ctx, pool, spec, opts)
		if err != nil {
			return rep, err
		}
		rep.RowsRotated += n
		rep.PerTable[spec.Table] = n
	}
	return rep, nil
}

// tableExists reports whether a table is present in the current database.
func tableExists(ctx context.Context, pool *pgxpool.Pool, table string) (bool, error) {
	var reg *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass($1)::text`, table).Scan(&reg); err != nil {
		return false, fmt.Errorf("check table %s exists: %w", table, err)
	}
	return reg != nil, nil
}

// selectSQL builds the FOR UPDATE candidate query for a table. It selects the
// PK as text (uniform across UUID and TEXT PKs) plus every cipher column, and
// filters to rows where at least one cipher column is non-null.
func selectSQL(spec TableSpec) string {
	cols := make([]string, len(spec.Columns))
	notNull := make([]string, len(spec.Columns))
	for i, c := range spec.Columns {
		cols[i] = c.Name
		notNull[i] = c.Name + " IS NOT NULL"
	}
	return fmt.Sprintf(
		"SELECT %s::text, %s FROM %s WHERE %s FOR UPDATE",
		spec.PKColumn, strings.Join(cols, ", "), spec.Table, strings.Join(notNull, " OR "),
	)
}

// rotateTable re-encrypts every candidate row in one transaction. In ModeDryRun
// it performs the full decrypt/encrypt but rolls back at the end. Returns the
// number of rows touched.
func rotateTable(ctx context.Context, pool *pgxpool.Pool, spec TableSpec, opts SweepOpts) (int, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin tx on %s: %w", spec.Table, err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after an explicit Commit

	rows, err := tx.Query(ctx, selectSQL(spec))
	if err != nil {
		return 0, fmt.Errorf("select %s: %w", spec.Table, err)
	}

	type update struct {
		pk      string
		newVals [][]byte // len == len(spec.Columns); nil for a NULL cell
	}
	var updates []update

	for rows.Next() {
		dest := make([]any, 1+len(spec.Columns))
		var pk string
		dest[0] = &pk
		rawByte := make([][]byte, len(spec.Columns))
		rawText := make([]string, len(spec.Columns))
		for i, c := range spec.Columns {
			if c.Encoding == EncodingHexText {
				dest[1+i] = &rawText[i]
			} else {
				dest[1+i] = &rawByte[i]
			}
		}
		if err := rows.Scan(dest...); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan %s row: %w", spec.Table, err)
		}

		u := update{pk: pk, newVals: make([][]byte, len(spec.Columns))}
		changed := false // did any cell in this row actually need re-encryption?
		for i, c := range spec.Columns {
			cell := rawByte[i]
			if c.Encoding == EncodingHexText {
				if rawText[i] == "" {
					continue // NULL/empty cell — leave as-is
				}
				decoded, derr := hex.DecodeString(rawText[i])
				if derr != nil {
					rows.Close()
					return 0, fmt.Errorf("%s.%s pk=%s: not valid hex: %w", spec.Table, c.Name, pk, derr)
				}
				cell = decoded
			}
			if len(cell) == 0 {
				continue // NULL cell
			}
			// Idempotency / resumability: a cell that already decrypts under the
			// new key was rotated by a previous run — skip it rather than
			// re-encrypt (which would fail, since it no longer decrypts under
			// OLD). This makes `rotate` safe to re-run and lets a
			// partially-completed multi-table rotation resume without stranding
			// the tables that already committed. Trial-decryption is
			// authoritative here; the kek_version stamp is only a cheap audit
			// marker, not the completion signal.
			if OnNewKey(opts.NewKey, cell) {
				continue
			}
			newCT, rerr := Rekey(opts.OldKey, opts.NewKey, cell)
			if rerr != nil {
				rows.Close()
				return 0, fmt.Errorf("%s.%s pk=%s: %w", spec.Table, c.Name, pk, rerr)
			}
			u.newVals[i] = newCT
			changed = true
		}
		// Rows whose cells are all already on the new key (or all NULL) need no
		// UPDATE and are not counted as rotated — that is what makes a re-run
		// report zero.
		if changed {
			updates = append(updates, u)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate %s: %w", spec.Table, err)
	}
	rows.Close()

	if opts.Mode == ModeDryRun {
		return len(updates), nil
	}

	for _, u := range updates {
		if err := applyUpdate(ctx, tx, spec, u.pk, u.newVals, opts.ToVersion); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit %s: %w", spec.Table, err)
	}
	slog.Info("rotate-kek: table rotated", "table", spec.Table, "rows", len(updates), "to_version", opts.ToVersion)
	return len(updates), nil
}

// applyUpdate writes the re-encrypted cells + version stamp for one row.
func applyUpdate(ctx context.Context, tx pgx.Tx, spec TableSpec, pk string, newVals [][]byte, toVersion int16) error {
	set := make([]string, 0, len(spec.Columns)+1)
	args := make([]any, 0, len(spec.Columns)+2)
	argN := 1
	for i, c := range spec.Columns {
		if newVals[i] == nil {
			continue // NULL cell was skipped
		}
		if c.Encoding == EncodingHexText {
			set = append(set, fmt.Sprintf("%s = $%d", c.Name, argN))
			args = append(args, hex.EncodeToString(newVals[i]))
		} else {
			set = append(set, fmt.Sprintf("%s = $%d", c.Name, argN))
			args = append(args, newVals[i])
		}
		argN++
	}
	set = append(set, fmt.Sprintf("%s = $%d", spec.VersionColumn, argN))
	args = append(args, toVersion)
	argN++
	args = append(args, pk)
	sql := fmt.Sprintf("UPDATE %s SET %s WHERE %s::text = $%d",
		spec.Table, strings.Join(set, ", "), spec.PKColumn, argN)
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		return fmt.Errorf("update %s pk=%s: %w", spec.Table, pk, err)
	}
	return nil
}

// verifyTable counts rows with at least one cipher cell that does not decrypt
// under newKey (i.e. still on the old key). It never mutates.
func verifyTable(ctx context.Context, pool *pgxpool.Pool, spec TableSpec, newKey []byte) (int, error) {
	rows, err := pool.Query(ctx, selectSQL(spec))
	if err != nil {
		return 0, fmt.Errorf("verify select %s: %w", spec.Table, err)
	}
	defer rows.Close()

	remaining := 0
	for rows.Next() {
		dest := make([]any, 1+len(spec.Columns))
		var pk string
		dest[0] = &pk
		rawByte := make([][]byte, len(spec.Columns))
		rawText := make([]string, len(spec.Columns))
		for i, c := range spec.Columns {
			if c.Encoding == EncodingHexText {
				dest[1+i] = &rawText[i]
			} else {
				dest[1+i] = &rawByte[i]
			}
		}
		if err := rows.Scan(dest...); err != nil {
			return 0, fmt.Errorf("verify scan %s: %w", spec.Table, err)
		}
		onOld := false
		for i, c := range spec.Columns {
			cell := rawByte[i]
			if c.Encoding == EncodingHexText {
				if rawText[i] == "" {
					continue
				}
				decoded, derr := hex.DecodeString(rawText[i])
				if derr != nil {
					onOld = true // undecodable ⇒ definitely not on the new key
					break
				}
				cell = decoded
			}
			if len(cell) == 0 {
				continue
			}
			if !OnNewKey(newKey, cell) {
				onOld = true
				break
			}
		}
		if onOld {
			remaining++
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("verify iterate %s: %w", spec.Table, err)
	}
	return remaining, nil
}

// NextVersion returns 1 + the maximum kek_version across all non-optional
// specs' tables (treating NULL as 0), so one rotation run stamps a single
// generation. Fresh tables (all NULL) yield 1.
func NextVersion(ctx context.Context, pool *pgxpool.Pool, specs []TableSpec) (int16, error) {
	var maxVer int16
	for _, spec := range specs {
		if spec.Optional {
			exists, err := tableExists(ctx, pool, spec.Table)
			if err != nil {
				return 0, err
			}
			if !exists {
				continue
			}
		}
		var v int16
		q := fmt.Sprintf("SELECT COALESCE(MAX(%s), 0) FROM %s", spec.VersionColumn, spec.Table)
		if err := pool.QueryRow(ctx, q).Scan(&v); err != nil {
			return 0, fmt.Errorf("max version %s: %w", spec.Table, err)
		}
		if v > maxVer {
			maxVer = v
		}
	}
	return maxVer + 1, nil
}

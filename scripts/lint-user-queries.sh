#!/usr/bin/env bash
# lint-user-queries.sh — CI guard for the kind-guard pattern (FE-API-048 §4.1).
#
# Fails if any SELECT query against the `users` table in
# services/auth/internal/repository/ is missing a kind filter, meaning it could
# silently return service-account shadow rows to callers on the human
# authentication path.
#
# What counts as "kind-guarded" (i.e. OK to pass CI):
#   1. The SQL line already contains `kind =` (e.g. AND kind = 'human').
#   2. The query lives inside GetUserAnyKind — the one explicitly kind-agnostic
#      helper whose name signals the caller's intent.
#   3. The Go line carries the inline annotation `// allow-any-kind`, signalling
#      that the author consciously chose to read any-kind rows.
#   4. The SQL line carries the inline annotation `-- allow-any-kind`.
#   5. The file is a _test.go file — test fixtures intentionally probe shadow
#      users to verify the guard contract; they are not application code.
#   6. The statement is a DELETE (not reading user data, so the kind guard is
#      not required by this rule).
#
# Tuning guide: if a new legitimate any-kind query is added, annotate the
# `FROM users` line with `// allow-any-kind` rather than widening this script.
# Only extend the exclusion list here for structural reasons (new test suffix,
# new helper function analogous to GetUserAnyKind, etc.).

set -euo pipefail

# Grep returns exit 1 when no lines match; `|| true` converts that to success
# so set -e doesn't abort the script before we can print the report.
BAD=$(grep -rnE 'FROM\s+users\b' services/auth/internal/repository/ \
        --include='*.go' \
        | grep -v '_test\.go:'     \
        | grep -v 'kind\s*='       \
        | grep -v 'GetUserAnyKind' \
        | grep -v 'DELETE FROM'    \
        | grep -v '// allow-any-kind' \
        | grep -v -- '-- allow-any-kind' \
        || true)

if [ -n "$BAD" ]; then
    echo "ERROR: Found SELECT queries against 'users' without a kind guard:"
    echo "$BAD"
    echo
    echo "Every query that reads from 'users' must either:"
    echo "  • Filter by kind at the SQL layer (e.g. AND kind = 'human')"
    echo "  • Live inside GetUserAnyKind (the explicit any-kind helper)"
    echo "  • Be annotated with '// allow-any-kind' on the same line as FROM users"
    echo
    echo "See FE-API-048 spec §4.1 for the full kind-guard contract."
    exit 1
fi

echo "OK — all users queries in services/auth/internal/repository/ are kind-guarded."

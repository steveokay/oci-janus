#!/usr/bin/env bash
# Creates additional PostgreSQL databases listed in POSTGRES_MULTIPLE_DATABASES.
# Run automatically by the postgres container on first boot via initdb.d.
# The primary database is whatever POSTGRES_DB is set to (defaults to POSTGRES_USER).
set -e

if [ -z "$POSTGRES_MULTIPLE_DATABASES" ]; then
  echo "POSTGRES_MULTIPLE_DATABASES not set — skipping multi-db init"
  exit 0
fi

echo "Creating additional databases: $POSTGRES_MULTIPLE_DATABASES"

for db in $(echo "$POSTGRES_MULTIPLE_DATABASES" | tr ',' ' '); do
  psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-SQL
    SELECT 'CREATE DATABASE "$db"'
    WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = '$db')
    \gexec
SQL
  echo "Database '$db' ready."
done

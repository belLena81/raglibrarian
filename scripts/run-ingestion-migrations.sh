#!/usr/bin/env sh
set -eu
umask 077

direction="${MIGRATION_DIRECTION:-up}"
case "$direction" in up|down) ;; *) echo 'MIGRATION_DIRECTION must be up or down' >&2; exit 2;; esac
credential_source="${PGPASSFILE:?PGPASSFILE is required}"
test -r "$credential_source" || { echo 'migration credential file is unreadable' >&2; exit 1; }
runtime_pgpass="$(mktemp /tmp/ingestion-pgpass.XXXXXX)"
sql_file="$(mktemp /tmp/ingestion-migrate.XXXXXX)"
trap 'rm -f "$runtime_pgpass" "$sql_file"' EXIT HUP INT TERM
cp "$credential_source" "$runtime_pgpass"
chmod 600 "$runtime_pgpass"
export PGPASSFILE="$runtime_pgpass"

if [ "$direction" = up ]; then files=$(find /migrations -maxdepth 1 -type f -name '*.up.sql' | sort); else files=$(find /migrations -maxdepth 1 -type f -name '*.down.sql' | sort -r); fi
test -n "$files" || { echo 'no ingestion migrations found' >&2; exit 1; }
{
  echo '\set ON_ERROR_STOP on'
  echo 'BEGIN;'
  echo "SET LOCAL lock_timeout = '5s';"
  echo "SET LOCAL statement_timeout = '30s';"
  echo "SELECT pg_advisory_xact_lock(hashtext('raglibrarian.ingestion.migrations'));"
  echo 'CREATE SCHEMA IF NOT EXISTS ingestion;'
  echo 'CREATE TABLE IF NOT EXISTS ingestion.schema_migrations (version TEXT PRIMARY KEY, checksum TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW());'
  echo 'REVOKE ALL ON ingestion.schema_migrations FROM ingestion_runtime;'
  if [ "$direction" = up ]; then
    echo "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ingestion_e2e') AS ingestion_e2e_exists \\gset"
    echo '\if :ingestion_e2e_exists'
    echo 'GRANT USAGE ON SCHEMA ingestion TO ingestion_e2e;'
    echo 'ALTER DEFAULT PRIVILEGES IN SCHEMA ingestion GRANT SELECT ON TABLES TO ingestion_e2e;'
    echo '\endif'
  fi
} > "$sql_file"
for file in $files; do
  name=$(basename "$file"); version=${name%%_*}; checksum=$(sha256sum "$file" | awk '{print $1}')
  {
    printf "\\set migration_version '%s'\n" "$version"; printf "\\set migration_checksum '%s'\n" "$checksum"
    if [ "$direction" = up ]; then
      echo "SELECT EXISTS (SELECT 1 FROM ingestion.schema_migrations WHERE version = :'migration_version' AND checksum <> :'migration_checksum') AS checksum_mismatch \\gset"
      echo '\if :checksum_mismatch'; echo "  \echo 'previously applied ingestion migration checksum changed'"; echo '  \quit 3'; echo '\endif'
      echo "SELECT NOT EXISTS (SELECT 1 FROM ingestion.schema_migrations WHERE version = :'migration_version') AS should_apply \\gset"
      echo '\if :should_apply'; printf '\ir %s\n' "$file"; echo "INSERT INTO ingestion.schema_migrations (version, checksum) VALUES (:'migration_version', :'migration_checksum');"; echo '\endif'
    else
      echo "SELECT EXISTS (SELECT 1 FROM ingestion.schema_migrations WHERE version = :'migration_version') AS should_apply \\gset"
      echo '\if :should_apply'; echo "DELETE FROM ingestion.schema_migrations WHERE version = :'migration_version';"; printf '\ir %s\n' "$file"; echo '\endif'
    fi
  } >> "$sql_file"
done
if [ "$direction" = up ]; then
  echo '\if :ingestion_e2e_exists' >> "$sql_file"
  echo 'GRANT SELECT ON ingestion.inbox, ingestion.jobs, ingestion.artifact_sets TO ingestion_e2e;' >> "$sql_file"
  echo '\endif' >> "$sql_file"
fi
echo 'COMMIT;' >> "$sql_file"
psql --no-password --file "$sql_file"
echo 'Ingestion migrations completed'

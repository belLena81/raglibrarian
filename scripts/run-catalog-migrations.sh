#!/usr/bin/env sh
set -eu
umask 077

direction="${MIGRATION_DIRECTION:-up}"
case "$direction" in up|down) ;; *) echo 'MIGRATION_DIRECTION must be up or down' >&2; exit 2;; esac
command -v psql >/dev/null 2>&1 || { echo 'psql is required' >&2; exit 1; }
command -v sha256sum >/dev/null 2>&1 || { echo 'sha256sum is required' >&2; exit 1; }
credential_source="${PGPASSFILE:?PGPASSFILE is required}"
test -r "$credential_source" || { echo 'migration credential file is unreadable' >&2; exit 1; }
runtime_pgpass="$(mktemp /tmp/catalog-pgpass.XXXXXX)"
sql_file="$(mktemp /tmp/catalog-migrate.XXXXXX)"
trap 'rm -f "$runtime_pgpass" "$sql_file"' EXIT HUP INT TERM
cp "$credential_source" "$runtime_pgpass"
chmod 600 "$runtime_pgpass"
export PGPASSFILE="$runtime_pgpass"

if [ "$direction" = up ]; then files=$(find /migrations -maxdepth 1 -type f -name '*.up.sql' | sort); else files=$(find /migrations -maxdepth 1 -type f -name '*.down.sql' | sort -r); fi
test -n "$files" || { echo 'no catalog migrations found' >&2; exit 1; }
{
  echo '\set ON_ERROR_STOP on'
  echo 'BEGIN;'
  echo "SET LOCAL lock_timeout = '5s';"
  echo "SET LOCAL statement_timeout = '30s';"
  echo "SELECT pg_advisory_xact_lock(hashtext('raglibrarian.catalog.migrations'));"
  echo 'CREATE SCHEMA IF NOT EXISTS catalog;'
  echo 'CREATE TABLE IF NOT EXISTS catalog.schema_migrations (version TEXT PRIMARY KEY, checksum TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW());'
  echo 'REVOKE ALL ON catalog.schema_migrations FROM catalog_runtime;'
  if [ "$direction" = up ]; then
    echo "SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'catalog' AND table_name IN ('books', 'outbox')) AND NOT EXISTS (SELECT 1 FROM catalog.schema_migrations WHERE version = '001') AS untracked_legacy \\gset"
    echo '\if :untracked_legacy'
    echo "  \\echo 'untracked catalog schema detected; recreate the development catalog database or volume'"
    echo '  \quit 3'
    echo '\endif'
  fi
} > "$sql_file"
for file in $files; do
  name=$(basename "$file"); version=${name%%_*}; checksum=$(sha256sum "$file" | awk '{print $1}')
  {
    printf "\\set migration_version '%s'\n" "$version"; printf "\\set migration_checksum '%s'\n" "$checksum"
    if [ "$direction" = up ]; then
      echo "SELECT EXISTS (SELECT 1 FROM catalog.schema_migrations WHERE version = :'migration_version' AND checksum <> :'migration_checksum') AS checksum_mismatch \\gset"
      echo '\if :checksum_mismatch'; echo "  \echo 'previously applied catalog migration checksum changed'"; echo '  \quit 3'; echo '\endif'
      echo "SELECT NOT EXISTS (SELECT 1 FROM catalog.schema_migrations WHERE version = :'migration_version') AS should_apply \\gset"
      echo '\if :should_apply'; printf '\ir %s\n' "$file"; echo "INSERT INTO catalog.schema_migrations (version, checksum) VALUES (:'migration_version', :'migration_checksum');"; echo '\endif'
    else
      echo "SELECT EXISTS (SELECT 1 FROM catalog.schema_migrations WHERE version = :'migration_version') AS should_apply \\gset"
      echo '\if :should_apply'; printf '\ir %s\n' "$file"; echo "DELETE FROM catalog.schema_migrations WHERE version = :'migration_version';"; echo '\endif'
    fi
  } >> "$sql_file"
done
echo 'COMMIT;' >> "$sql_file"
psql --no-password --file "$sql_file"
echo 'Catalog migrations completed'

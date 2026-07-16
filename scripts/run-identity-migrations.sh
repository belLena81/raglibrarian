#!/usr/bin/env sh
set -eu
umask 077

direction="${MIGRATION_DIRECTION:-up}"
case "$direction" in
  up|down) ;;
  *) echo "MIGRATION_DIRECTION must be up or down" >&2; exit 2 ;;
esac

command -v psql >/dev/null 2>&1 || { echo "psql is required" >&2; exit 1; }
command -v sha256sum >/dev/null 2>&1 || { echo "sha256sum is required" >&2; exit 1; }
credential_source="${PGPASSFILE:?PGPASSFILE is required}"
test -r "$credential_source" || { echo "migration credential file is unreadable" >&2; exit 1; }
runtime_pgpass="$(mktemp /tmp/identity-pgpass.XXXXXX)"
cp "$credential_source" "$runtime_pgpass"
chmod 600 "$runtime_pgpass"
export PGPASSFILE="$runtime_pgpass"

sql_file="$(mktemp /tmp/identity-migrate.XXXXXX)"
trap 'rm -f "$sql_file" "$runtime_pgpass"' EXIT HUP INT TERM

{
  echo '\set ON_ERROR_STOP on'
  echo 'BEGIN;'
  echo "SET LOCAL lock_timeout = '5s';"
  echo "SET LOCAL statement_timeout = '30s';"
  echo "SELECT pg_advisory_xact_lock(hashtext('raglibrarian.identity.migrations'));"
  echo 'CREATE TABLE IF NOT EXISTS identity.schema_migrations ('
  echo '  version TEXT PRIMARY KEY,'
  echo '  checksum TEXT NOT NULL,'
  echo '  applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()'
  echo ');'
  echo 'REVOKE ALL ON identity.schema_migrations FROM identity_runtime;'
} > "$sql_file"

if [ "$direction" = up ]; then
  files=$(find /migrations -maxdepth 1 -type f -name '*.up.sql' | sort)
else
  files=$(find /migrations -maxdepth 1 -type f -name '*.down.sql' | sort -r)
fi

test -n "$files" || { echo "no identity migration files found" >&2; exit 1; }

for file in $files; do
  name=$(basename "$file")
  version=${name%%_*}
  checksum=$(sha256sum "$file" | awk '{print $1}')
  {
    printf "\\set migration_version '%s'\n" "$version"
    printf "\\set migration_checksum '%s'\n" "$checksum"
    if [ "$direction" = up ]; then
      echo "SELECT EXISTS (SELECT 1 FROM identity.schema_migrations WHERE version = :'migration_version' AND checksum <> :'migration_checksum') AS checksum_mismatch \\gset"
      echo '\if :checksum_mismatch'
      echo "  \\echo 'previously applied identity migration checksum changed'"
      echo '  \quit 3'
      echo '\endif'
      echo "SELECT NOT EXISTS (SELECT 1 FROM identity.schema_migrations WHERE version = :'migration_version') AS should_apply \\gset"
      echo '\if :should_apply'
      printf '\\ir %s\n' "$file"
      echo "INSERT INTO identity.schema_migrations (version, checksum) VALUES (:'migration_version', :'migration_checksum');"
      echo '\endif'
    else
      echo "SELECT EXISTS (SELECT 1 FROM identity.schema_migrations WHERE version = :'migration_version') AS should_apply \\gset"
      echo '\if :should_apply'
      printf '\\ir %s\n' "$file"
      echo "DELETE FROM identity.schema_migrations WHERE version = :'migration_version';"
      echo '\endif'
    fi
  } >> "$sql_file"
done

echo 'COMMIT;' >> "$sql_file"
psql --no-password --file "$sql_file"
echo "Identity migrations completed"

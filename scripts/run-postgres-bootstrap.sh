#!/usr/bin/env sh
set -eu
umask 077

credential_source="${PGPASSFILE:?PGPASSFILE is required}"
bootstrap_sql_file="${BOOTSTRAP_SQL_FILE:?BOOTSTRAP_SQL_FILE is required}"
bootstrap_complete_message="${BOOTSTRAP_COMPLETE_MESSAGE:-Database roles and privileges are ready}"
test -r "$credential_source" || { echo "bootstrap credential file is unreadable" >&2; exit 1; }
test -r "$bootstrap_sql_file" || { echo "bootstrap SQL file is unreadable" >&2; exit 1; }
runtime_pgpass="$(mktemp /tmp/postgres-pgpass.XXXXXX)"
trap 'rm -f "$runtime_pgpass"' EXIT HUP INT TERM
cp "$credential_source" "$runtime_pgpass"
chmod 600 "$runtime_pgpass"
export PGPASSFILE="$runtime_pgpass"

psql --no-password --file "$bootstrap_sql_file"
echo "$bootstrap_complete_message"

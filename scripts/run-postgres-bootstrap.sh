#!/usr/bin/env sh
set -eu
umask 077

credential_source="${PGPASSFILE:?PGPASSFILE is required}"
test -r "$credential_source" || { echo "bootstrap credential file is unreadable" >&2; exit 1; }
runtime_pgpass="$(mktemp /tmp/postgres-pgpass.XXXXXX)"
trap 'rm -f "$runtime_pgpass"' EXIT HUP INT TERM
cp "$credential_source" "$runtime_pgpass"
chmod 600 "$runtime_pgpass"
export PGPASSFILE="$runtime_pgpass"

psql --no-password --file /bootstrap/identity.sql
echo "Identity database roles and privileges are ready"

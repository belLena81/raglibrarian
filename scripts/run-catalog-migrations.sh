#!/usr/bin/env bash
set -euo pipefail

: "${PGHOST:?PGHOST is required}"
: "${PGDATABASE:?PGDATABASE is required}"
: "${PGUSER:?PGUSER is required}"
: "${PGPASSFILE:?PGPASSFILE is required}"

direction="${MIGRATION_DIRECTION:-up}"
case "$direction" in
  up) pattern='*.up.sql' ;;
  down) pattern='*.down.sql' ;;
  *) echo 'MIGRATION_DIRECTION must be up or down' >&2; exit 1 ;;
esac

shopt -s nullglob
files=(/migrations/$pattern)
if ((${#files[@]} == 0)); then
  echo 'no catalog migrations found' >&2
  exit 1
fi
if [[ "$direction" == down ]]; then
  for ((i=${#files[@]}-1; i>=0; i--)); do psql -v ON_ERROR_STOP=1 -f "${files[i]}"; done
else
  for file in "${files[@]}"; do psql -v ON_ERROR_STOP=1 -f "$file"; done
fi

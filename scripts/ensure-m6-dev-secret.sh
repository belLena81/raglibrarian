#!/usr/bin/env bash
set -euo pipefail
umask 077

dir="${1:-${SECRET_DIR:-.dev/secrets}}"
file="$dir/answer_llm_test_api_key"
mkdir -p "$dir"
chmod 700 "$dir"

if [[ -r "$file" ]]; then
  [[ ! -L "$file" && -f "$file" && "$(stat -c '%a' "$file")" == 400 ]] || {
    echo 'Existing M6 test provider key is not a private regular file' >&2
    exit 1
  }
  exit 0
fi
[[ ! -e "$file" ]] || { echo 'Incomplete M6 test provider key; refusing overwrite' >&2; exit 1; }

openssl rand -hex 32 > "$file"
chmod 400 "$file"

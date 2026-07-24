#!/usr/bin/env bash
set -euo pipefail
umask 077

secret_dir="${1:-${SECRET_DIR:-.dev/secrets}}"
key_file="${2:-${ANSWER_LLM_API_KEY_PATH:-$secret_dir/answer_llm_api_key}}"
mkdir -p "$secret_dir"
chmod 700 "$secret_dir"

if [[ -r "$key_file" ]]; then
  [[ ! -L "$key_file" && -f "$key_file" && "$(stat -c '%a' "$key_file")" == 400 ]] || {
    echo 'Existing answer provider key is not a regular 0400 secret file' >&2
    exit 1
  }
  exit 0
fi

[[ ! -e "$key_file" ]] || {
  echo "Existing answer provider key path is not a regular readable file: $key_file" >&2
  exit 1
}

command -v openssl >/dev/null || { echo 'openssl is required' >&2; exit 1; }
openssl rand -hex 32 > "$key_file"
chmod 400 "$key_file"
echo "Generated local answer provider key: $key_file"

#!/usr/bin/env bash
set -euo pipefail

dir="${1:-${SECRET_DIR:-.dev/secrets}}"
if bash ./scripts/check-m5-dev-secrets.sh "$dir" >/dev/null 2>&1; then
  exit 0
fi

existing=$(find "$dir" -maxdepth 1 -type f -name 'retrieval_*' -print -quit 2>/dev/null || true)
[[ -z "$existing" ]] || {
  echo "Incomplete M5 secret set in $dir; refusing an automatic partial overwrite" >&2
  exit 1
}
bash ./scripts/generate-m5-dev-secrets.sh "$dir"

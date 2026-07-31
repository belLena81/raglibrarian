#!/usr/bin/env bash
set -euo pipefail
umask 077

dir="${1:-${SECRET_DIR:-.dev/secrets}}"
access_file="$dir/layout_minio_access_key"
secret_file="$dir/layout_minio_secret_key"

if [[ ! -d "$dir" || -L "$dir" || "$(stat -c '%a' "$dir")" != 700 ]]; then
  echo "Layout worker secret directory must be a non-symlink directory with mode 0700: $dir" >&2
  exit 1
fi

present=0
[[ ! -e "$access_file" ]] || present=$((present + 1))
[[ ! -e "$secret_file" ]] || present=$((present + 1))
if ((present == 1)); then
  echo "refusing to modify a partial layout worker secret set in $dir" >&2
  exit 1
fi
if ((present == 0)); then
  command -v openssl >/dev/null || { echo "openssl is required" >&2; exit 1; }
  printf '%s\n' layout-parser-worker > "$access_file"
  openssl rand -hex 32 > "$secret_file"
  chmod 400 "$access_file" "$secret_file"
fi

for path in "$access_file" "$secret_file"; do
  if [[ ! -f "$path" || -L "$path" || ! -r "$path" || "$(stat -c '%a' "$path")" != 400 ]]; then
    echo "Layout worker secret must be a readable non-symlink regular file with mode 0400: $path" >&2
    exit 1
  fi
done
[[ "$(<"$access_file")" == layout-parser-worker && -s "$secret_file" ]] || {
  echo "Layout worker MinIO credentials are invalid" >&2
  exit 1
}

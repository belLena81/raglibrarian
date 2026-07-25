#!/usr/bin/env bash
set -euo pipefail
umask 077

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

command -v curl >/dev/null 2>&1 || { echo "curl is required for host ingestion assets" >&2; exit 1; }
command -v go >/dev/null 2>&1 || { echo "go is required for host ingestion assets" >&2; exit 1; }
command -v sha256sum >/dev/null 2>&1 || { echo "sha256sum is required for host ingestion assets" >&2; exit 1; }

asset_dir="${HOST_ASSET_DIR:-.dev/host-assets}"
bin_dir="$asset_dir/bin"
tokenizer_path="$asset_dir/cl100k_base.tiktoken"
tokenizer_sha256="223921b76ee99bde995b7ff738513eef100fb51d18c93597a113bcffe865b2a7"

mkdir -p "$bin_dir"
chmod 700 "$asset_dir" "$bin_dir"

if [[ ! -r "$tokenizer_path" ]] || ! printf '%s  %s\n' "$tokenizer_sha256" "$tokenizer_path" | sha256sum -c >/dev/null 2>&1; then
  tmp_file="$(mktemp "$asset_dir/.tokenizer.XXXXXX")"
  trap 'rm -f "$tmp_file"' EXIT
  curl -fsSL "https://openaipublic.blob.core.windows.net/encodings/cl100k_base.tiktoken" -o "$tmp_file"
  printf '%s  %s\n' "$tokenizer_sha256" "$tmp_file" | sha256sum -c >/dev/null
  chmod 400 "$tmp_file"
  mv -f "$tmp_file" "$tokenizer_path"
  trap - EXIT
fi

GOCACHE="${GOCACHE:-/tmp/raglibrarian-go-cache}" \
  go build -o "$bin_dir/epub-parser" ./services/ingestion-service/cmd/epub_parser
chmod 500 "$bin_dir/epub-parser"

GOCACHE="${GOCACHE:-/tmp/raglibrarian-go-cache}" \
  go build -o "$bin_dir/parser-sandbox" ./services/ingestion-service/cmd/parser_sandbox
chmod 500 "$bin_dir/parser-sandbox"

echo "Host ingestion assets ready in $asset_dir"

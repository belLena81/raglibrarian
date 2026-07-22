#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

for command in openssl stat; do
  command -v "$command" >/dev/null 2>&1 || {
    echo "$command is required for M6 development configuration tests" >&2
    exit 1
  }
done

test_root="$(mktemp -d /tmp/raglibrarian-m6-config.XXXXXX)"
trap 'rm -rf "$test_root"' EXIT
cert_dir="$test_root/certs"
secret_dir="$test_root/secrets"

bash ./scripts/generate-dev-certs.sh "$cert_dir" >/dev/null
bash ./scripts/ensure-m6-dev-cert.sh "$cert_dir" >/dev/null
bash ./scripts/ensure-m6-dev-secret.sh "$secret_dir" >/dev/null

for file in answer-service.crt answer-service.key llm-provider-stub.crt llm-provider-stub.key; do
  [[ -r "$cert_dir/$file" ]] || { echo "M6 certificate material is missing: $file" >&2; exit 1; }
  [[ "$(stat -c '%a' "$cert_dir/$file")" == 600 ]] || { echo "M6 certificate mode is not 0600: $file" >&2; exit 1; }
done
[[ "$(stat -c '%a' "$secret_dir/answer_llm_test_api_key")" == 400 ]] || {
  echo "M6 provider key mode is not 0400" >&2
  exit 1
}

cert_hash="$(openssl x509 -in "$cert_dir/answer-service.crt" -noout -fingerprint -sha256)"
secret_hash="$(openssl dgst -sha256 "$secret_dir/answer_llm_test_api_key")"
bash ./scripts/ensure-m6-dev-cert.sh "$cert_dir" >/dev/null
bash ./scripts/ensure-m6-dev-secret.sh "$secret_dir" >/dev/null
[[ "$(openssl x509 -in "$cert_dir/answer-service.crt" -noout -fingerprint -sha256)" == "$cert_hash" ]] || {
  echo "M6 certificate was unexpectedly replaced" >&2
  exit 1
}
[[ "$(openssl dgst -sha256 "$secret_dir/answer_llm_test_api_key")" == "$secret_hash" ]] || {
  echo "M6 provider key was unexpectedly replaced" >&2
  exit 1
}

echo "M6 development configuration regressions passed"

#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

for command in docker jq openssl stat; do
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

compose_config="$({
	CERT_DIR="$cert_dir" \
	SECRET_DIR="$secret_dir" \
	ANSWER_LLM_API_KEY_PATH="$secret_dir/answer_llm_test_api_key" \
	ANSWER_LLM_BASE_URL= \
	ANSWER_LLM_MODEL= \
	docker compose -f docker-compose.yml -f docker-compose.ci.yml \
		--profile m4-ha --profile m5 --profile m6 --profile m6-test config --no-env-resolution --format json
})"
printf '%s' "$compose_config" | jq -e '
	.services["llm-provider-stub"].user == null and
	(.services["llm-provider-stub"].cap_add | sort) == ["DAC_READ_SEARCH", "SETGID", "SETUID"] and
	.services["llm-provider-stub"].environment.RUN_AS_UID == "65532" and
	.services["llm-provider-stub"].environment.RUN_AS_GID == "65532" and
	.services["llm-provider-stub"].healthcheck.test == ["CMD", "/healthcheck"] and
	.services["answer-service"].depends_on["llm-provider-stub"].condition == "service_healthy" and
	.services["edge-api"].environment.EDGE_ANSWER_RATE_LIMIT == "30" and
	.services["edge-api-2"].environment.EDGE_ANSWER_RATE_LIMIT == "30"
' >/dev/null || {
	echo "M6 Compose security or test-limit configuration regressed" >&2
	exit 1
}

bash ./scripts/test-m6-real-provider-gate.sh

echo "M6 development configuration regressions passed"

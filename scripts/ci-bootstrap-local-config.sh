#!/usr/bin/env bash
set -euo pipefail

cp .env.example .env
make dev-secrets dev-certs
bash ./scripts/ensure-m5-dev-cert.sh
bash ./scripts/ensure-m6-dev-cert.sh

umask 077
openssl rand -base64 24 | tr -d '\n' > .dev/secrets/e2e_bootstrap_code
{ printf 'raglibrarian/admin-bootstrap/v1\0'; cat .dev/secrets/e2e_bootstrap_code; } \
  | openssl dgst -sha256 -binary > .dev/secrets/identity_bootstrap_verifier
chmod 400 .dev/secrets/e2e_bootstrap_code .dev/secrets/identity_bootstrap_verifier

if [ -n "${GITHUB_ENV:-}" ]; then
	echo "ANSWER_LLM_API_KEY_PATH=.dev/secrets/answer_llm_test_api_key" >> "$GITHUB_ENV"
fi

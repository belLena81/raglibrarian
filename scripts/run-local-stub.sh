#!/usr/bin/env bash
set -euo pipefail
umask 077

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

for command in docker curl npm; do
	command -v "$command" >/dev/null 2>&1 || {
		echo "$command is required for local stub run" >&2
		exit 1
	}
done
docker compose version >/dev/null

if [[ ! -f .env ]]; then
	cp .env.example .env
	echo "Created .env from .env.example. Review loopback ports if needed."
fi

secret_dir="${SECRET_DIR:-.dev/secrets}"
cert_dir="${CERT_DIR:-.dev/certs}"

if [[ ! -r "$secret_dir/identity_runtime_dsn" ]]; then
	if [[ -d "$secret_dir" ]] && [[ -n "$(find "$secret_dir" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
		echo "Incomplete local secrets in $secret_dir; do not overwrite them automatically." >&2
		echo "Remove the directory only if you intend a full local reset, then rerun this script." >&2
		exit 1
	fi
	make dev-secrets
elif [[ ! -r "$secret_dir/catalog_minio_access_key" ]]; then
	make dev-secrets-m3
fi

bash ./scripts/ensure-m4-dev-secrets.sh "$secret_dir"
bash ./scripts/ensure-m5-dev-secrets.sh "$secret_dir"
bash ./scripts/ensure-m6-answer-provider-key.sh "$secret_dir"

if [[ ! -r "$secret_dir/identity_bootstrap_verifier" ]]; then
	echo "Creating a local admin bootstrap verifier (interactive)."
	echo "The one-time bootstrap code is printed below; store it now."
	make bootstrap-verifier
	echo "Use the code only with /setup/admin, then remove the verifier after setup."
fi

if [[ ! -r "$cert_dir/ca.crt" ]]; then
	if [[ -d "$cert_dir" ]] && [[ -n "$(find "$cert_dir" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
		echo "Incomplete local certificates in $cert_dir; do not overwrite them automatically." >&2
		echo "Remove the directory only if you intend a full local reset, then rerun this script." >&2
		exit 1
	fi
	make dev-certs
fi
bash ./scripts/ensure-m6-dev-cert.sh "$cert_dir"

compose_project="${COMPOSE_PROJECT_NAME:-raglibrarian-local}"
docker compose -f docker-compose.yml -f docker-compose.ci.yml --profile m5 --profile m6 --profile m6-test config --quiet
COMPOSE_PROJECT_NAME="$compose_project" \
	docker compose -f docker-compose.yml -f docker-compose.ci.yml --profile m5 --profile m6 --profile m6-test up -d --build --wait --wait-timeout 300

if [[ ! -d ui/node_modules ]]; then
	npm --prefix ui ci
fi

wait_for_backend() {
	for _ in {1..30}; do
		if curl --fail --silent --show-error http://127.0.0.1:8080/readyz >/dev/null; then
			return
		fi
		sleep 1
	done
	curl --fail --silent --show-error http://127.0.0.1:8080/readyz >/dev/null
}

wait_for_backend

echo "Backend ready: http://127.0.0.1:8080"
echo "Mailpit:       http://127.0.0.1:${MAILPIT_UI_PORT:-8025}"
echo "Compose down (all profiles): COMPOSE_PROJECT_NAME=$compose_project docker compose -f docker-compose.yml -f docker-compose.ci.yml --profile m5 --profile m6 --profile m6-test down -v --remove-orphans"
echo "Backend logs:  docker compose -f docker-compose.yml -f docker-compose.ci.yml --profile m5 --profile m6 --profile m6-test logs -f"

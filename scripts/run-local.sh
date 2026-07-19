#!/usr/bin/env bash
set -euo pipefail
umask 077

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

regenerate_bootstrap_code=false
case "${1:-}" in
  "")
    ;;
  --regenerate-bootstrap-code)
    regenerate_bootstrap_code=true
    ;;
  *)
    echo "usage: $0 [--regenerate-bootstrap-code]" >&2
    exit 1
    ;;
esac

for command in docker npm curl; do
  command -v "$command" >/dev/null 2>&1 || {
    echo "$command is required for a local run" >&2
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

# Additive development secrets must not force a destructive local reset. This
# key is independent of existing credentials and is created only when absent.
if [[ ! -r "$secret_dir/identity_password_reset_hmac_key" ]]; then
  command -v openssl >/dev/null 2>&1 || {
    echo "openssl is required to create the password-reset development secret" >&2
    exit 1
  }
  umask 077
  openssl rand -hex 32 > "$secret_dir/identity_password_reset_hmac_key"
  chmod 400 "$secret_dir/identity_password_reset_hmac_key"
  echo "Created missing password-reset development secret."
fi

bash ./scripts/ensure-m4-dev-secrets.sh "$secret_dir"

if [[ ! -r "$secret_dir/identity_bootstrap_verifier" ]]; then
  echo "Creating a local admin bootstrap verifier (interactive)."
  echo "The one-time bootstrap code is printed below; store it now."
  make bootstrap-verifier
  echo "Use the code only with /setup/admin, then remove the verifier after setup."
elif [[ "$regenerate_bootstrap_code" == false ]]; then
  echo "Admin bootstrap verifier already exists; its code cannot be displayed or recovered."
fi

if [[ ! -r "$cert_dir/ca.crt" ]]; then
	if [[ -d "$cert_dir" ]] && [[ -n "$(find "$cert_dir" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
    echo "Incomplete local certificates in $cert_dir; do not overwrite them automatically." >&2
    echo "Remove the directory only if you intend a full local reset, then rerun this script." >&2
    exit 1
  fi
  make dev-certs
fi

make compose-config
docker compose up -d --build --wait --wait-timeout 180

log_pid_dir=.dev/log-pids
mkdir -p "$log_pid_dir"
for service in edge-api identity-service catalog-service ingestion-service; do
  service_log_dir="_logs/$service"
  service_log_file="$service_log_dir/service.log"
  service_pid_file="$log_pid_dir/$service.pid"
  mkdir -p "$service_log_dir"

  if [[ -r "$service_pid_file" ]] && kill -0 "$(cat "$service_pid_file")" 2>/dev/null; then
    continue
  fi

  rm -f "$service_pid_file"
  nohup docker compose logs --no-color --follow "$service" >>"$service_log_file" 2>&1 &
  echo "$!" >"$service_pid_file"
done

if [[ ! -d ui/node_modules ]]; then
  npm --prefix ui ci
fi

ui_pid_file=.dev/ui.pid
ui_log_file=.dev/ui.log
if [[ -r "$ui_pid_file" ]] && kill -0 "$(cat "$ui_pid_file")" 2>/dev/null; then
  echo "UI already running (PID $(cat "$ui_pid_file"))."
else
  rm -f "$ui_pid_file"
  (
    cd ui
    nohup npm run dev -- --host 127.0.0.1 >"$root_dir/$ui_log_file" 2>&1 &
    echo "$!" >"$root_dir/$ui_pid_file"
  )
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

if [[ "$regenerate_bootstrap_code" == true ]]; then
  setup_status="$(curl --fail --silent --show-error http://127.0.0.1:8080/setup/status)"
  if [[ "$setup_status" != '{"required":true}' ]]; then
    echo "Bootstrap is unavailable because an administrator is already configured." >&2
    exit 1
  fi
  rm -f "$secret_dir/identity_bootstrap_verifier"
  echo "Creating a replacement local admin bootstrap verifier (interactive)."
  echo "The one-time bootstrap code is printed below; store it now."
  make bootstrap-verifier
  docker compose up -d --force-recreate identity-service
  wait_for_backend
fi

echo "Backend ready: http://127.0.0.1:8080"
echo "UI:            http://127.0.0.1:5173"
echo "Mailpit:       http://127.0.0.1:${MAILPIT_UI_PORT:-8025}"
echo "Backend logs:  $root_dir/_logs/{edge-api,identity-service,catalog-service,ingestion-service}/service.log"
echo "Stop backend with: docker compose down"
echo "Stop local stack:  bash ./scripts/stop-local.sh"

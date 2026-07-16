#!/usr/bin/env bash
set -euo pipefail
umask 077

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

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

if [[ ! -r "$secret_dir/identity_bootstrap_verifier" ]]; then
  echo "Create the local admin bootstrap verifier (interactive):"
  make bootstrap-verifier
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

for _ in {1..30}; do
  if curl --fail --silent --show-error http://127.0.0.1:8080/readyz >/dev/null; then
    break
  fi
  sleep 1
done
curl --fail --silent --show-error http://127.0.0.1:8080/readyz >/dev/null

echo "Backend ready: http://127.0.0.1:8080"
echo "UI:            http://127.0.0.1:5173"
echo "Mailpit:       http://127.0.0.1:${MAILPIT_UI_PORT:-8025}"
echo "Stop backend with: docker compose down"
echo "Stop UI with:      kill \$(cat $ui_pid_file)"

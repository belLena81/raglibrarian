#!/usr/bin/env bash
set -euo pipefail
umask 077

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
runtime_dir="${1:-$repo_root/deploy/cloud-test/runtime}"
secret_dir="$runtime_dir/secrets"
cert_dir="$runtime_dir/certs"
model_dir="$runtime_dir/models/m5-jina-code-v1"

for command in go jq openssl; do
  command -v "$command" >/dev/null || { echo "$command is required" >&2; exit 1; }
done

private_directory() {
  local path="$1"
  [[ -d "$path" && ! -L "$path" && "$(stat -c '%u' "$path")" == "$(id -u)" ]] || {
    echo "private runtime directory is unsafe: $path" >&2
    exit 1
  }
  local mode
  mode="$(stat -c '%a' "$path")"
  (( (8#$mode & 8#077) == 0 )) || { echo "private runtime directory must be owner-only: $path" >&2; exit 1; }
}

[[ ! -L "$runtime_dir" ]] || { echo "runtime directory must not be a symlink" >&2; exit 1; }
mkdir -p "$runtime_dir"
chmod 700 "$runtime_dir"
private_directory "$runtime_dir"

for directory in "$secret_dir" "$cert_dir"; do
  [[ ! -L "$directory" ]] || { echo "runtime subdirectory must not be a symlink: $directory" >&2; exit 1; }
done

if [[ ! -d "$secret_dir" ]]; then
  bash "$repo_root/scripts/generate-dev-secrets.sh" "$secret_dir"
fi
bash "$repo_root/scripts/ensure-m4-dev-secrets.sh" "$secret_dir"
bash "$repo_root/scripts/ensure-m5-dev-secrets.sh" "$secret_dir"
private_directory "$secret_dir"

if [[ ! -r "$secret_dir/identity_bootstrap_verifier" ]]; then
  echo "Create the one-time administrator bootstrap code now; do not save it in logs or Git."
  SECRET_DIR="$secret_dir" make -C "$repo_root" bootstrap-verifier
fi

if [[ ! -d "$cert_dir" ]]; then
  bash "$repo_root/scripts/generate-dev-certs.sh" "$cert_dir"
fi
bash "$repo_root/scripts/ensure-m5-dev-cert.sh" "$cert_dir"
bash "$repo_root/scripts/ensure-m6-dev-cert.sh" "$cert_dir"
private_directory "$cert_dir"

if [[ ! -r "$model_dir/.revision" ]]; then
  echo "The pinned embedding model is not installed."
  echo "Install the Hugging Face hf CLI, then run:"
  echo "M5_MODEL_DIR=$model_dir bash $repo_root/scripts/bootstrap-m5-model.sh"
fi

echo "Internal runtime secrets and mTLS certificates are ready in $runtime_dir"
echo "Next, install provider credentials with install_provider_secrets.sh"

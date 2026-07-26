#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
temp_dir="$(mktemp -d)"
trap 'rm -rf "$temp_dir"' EXIT

model_dir="$temp_dir/model"
revision=5e233c43ad83ba072172bca158a7c7dec46302a0
mkdir -p "$model_dir/nested" "$model_dir/onnx"
printf '%s\n' "$revision" > "$model_dir/.revision"
printf 'synthetic model fixture\n' > "$model_dir/model.safetensors"
printf 'synthetic config fixture\n' > "$model_dir/config.json"
printf 'fixture\n' > "$model_dir/nested/config.json"
printf 'synthetic onnx fixture\n' > "$model_dir/onnx/model.onnx"
chmod 0700 "$model_dir" "$model_dir/nested" "$model_dir/onnx"
chmod 0600 "$model_dir/.revision" "$model_dir/config.json" "$model_dir/model.safetensors" "$model_dir/nested/config.json" "$model_dir/onnx/model.onnx"

M5_MODEL_DIR="$model_dir" bash "$repo_root/scripts/bootstrap-m5-model.sh"

while IFS= read -r -d '' directory; do
  [[ "$(stat -c '%a' "$directory")" == "755" ]] || {
    echo "model directory permissions were not normalized: $directory" >&2
    exit 1
  }
done < <(find "$model_dir" -type d -print0)

while IFS= read -r -d '' file; do
  [[ "$(stat -c '%a' "$file")" == "444" ]] || {
    echo "model file permissions were not normalized: $file" >&2
    exit 1
  }
done < <(find "$model_dir" -type f -print0)

docker_test_dir="$(mktemp -d)"
trap 'rm -rf "$temp_dir" "$docker_test_dir"' EXIT
mkdir -p "$docker_test_dir/bin"

cat > "$docker_test_dir/bin/python3" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
chmod 0755 "$docker_test_dir/bin/python3"

cat > "$docker_test_dir/bin/docker" <<'EOF'
#!/usr/bin/env bash
log_file="${DOCKER_LOG_FILE:?}"
printf '%s\n' "$*" > "$log_file"
for arg in "$@"; do
  case "$arg" in
    *src=*)
      model_dir="${arg#*src=}"
      model_dir="${model_dir%%,*}"
      ;;
  esac
done
mkdir -p "$model_dir/onnx"
printf 'synthetic config fixture\n' > "$model_dir/config.json"
printf 'synthetic safetensors fixture\n' > "$model_dir/model.safetensors"
printf 'synthetic onnx fixture\n' > "$model_dir/onnx/model.onnx"
cat >/dev/null
exit 0
EOF
chmod 0755 "$docker_test_dir/bin/docker"

docker_log_file="$docker_test_dir/docker-argv"
HF_TOKEN=hf_test_token HF_ENDPOINT=https://hf.example.internal M5_MODEL_DIR="$docker_test_dir/model" DOCKER_LOG_FILE="$docker_log_file" PATH="$docker_test_dir/bin:/usr/bin:/bin" bash "$repo_root/scripts/bootstrap-m5-model.sh" || {
  echo "bootstrap script failed in docker fallback test" >&2
  exit 1
}

grep -F -- '-e HF_TOKEN=hf_test_token' "$docker_log_file" >/dev/null || {
  echo "docker fallback did not forward HF_TOKEN" >&2
  exit 1
}

grep -F -- '-e HF_ENDPOINT=https://hf.example.internal' "$docker_log_file" >/dev/null || {
  echo "docker fallback did not forward HF_ENDPOINT" >&2
  exit 1
}

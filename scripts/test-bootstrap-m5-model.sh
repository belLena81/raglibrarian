#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
temp_dir="$(mktemp -d)"
trap 'rm -rf "$temp_dir"' EXIT

model_dir="$temp_dir/model"
revision=516f4baf13dec4ddddda8631e019b5737c8bc250
mkdir -p "$model_dir/nested" "$model_dir/onnx"
printf '%s\n' "$revision" > "$model_dir/.revision"
printf 'synthetic model fixture\n' > "$model_dir/model.safetensors"
printf 'fixture\n' > "$model_dir/nested/config.json"
printf 'synthetic onnx fixture\n' > "$model_dir/onnx/model.onnx"
chmod 0700 "$model_dir" "$model_dir/nested" "$model_dir/onnx"
chmod 0600 "$model_dir/.revision" "$model_dir/model.safetensors" "$model_dir/nested/config.json" "$model_dir/onnx/model.onnx"

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

#!/usr/bin/env bash
set -euo pipefail
umask 077

command -v hf >/dev/null || {
  echo 'The pinned Hugging Face hf CLI is required to bootstrap the M5 model cache.' >&2
  echo 'Install it deliberately, then rerun make m5-model-bootstrap.' >&2
  exit 1
}

model_dir="${M5_MODEL_DIR:-.dev/models/m5-jina-code-v1}"
revision=516f4baf13dec4ddddda8631e019b5737c8bc250
if [[ -d "$model_dir" && -f "$model_dir/.revision" ]] && [[ "$(cat "$model_dir/.revision")" == "$revision" ]]; then
  find "$model_dir" -type l -print -quit | grep -q . && { echo 'Model cache must not contain symlinks' >&2; exit 1; }
  find "$model_dir" -type f -name '*.safetensors' -print -quit | grep -q . || { echo 'Pinned safetensors are missing' >&2; exit 1; }
  exit 0
fi
[[ ! -e "$model_dir" ]] || { echo "Refusing to replace incomplete model cache: $model_dir" >&2; exit 1; }

mkdir -p "$model_dir"
chmod 700 "$model_dir"
HF_HUB_DISABLE_TELEMETRY=1 hf download jinaai/jina-embeddings-v2-base-code \
  --revision "$revision" --local-dir "$model_dir" \
  --include '*.json' '*.txt' '*.safetensors'
find "$model_dir" -type l -print -quit | grep -q . && { echo 'Downloaded model cache contains symlinks' >&2; exit 1; }
find "$model_dir" -type f -name '*.safetensors' -print -quit | grep -q . || { echo 'Pinned safetensors were not downloaded' >&2; exit 1; }
printf '%s\n' "$revision" > "$model_dir/.revision"
chmod -R go-rwx "$model_dir"

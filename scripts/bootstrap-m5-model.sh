#!/usr/bin/env bash
set -euo pipefail
umask 077

model_dir="${M5_MODEL_DIR:-.dev/models/m5-jina-code-v1}"
revision=516f4baf13dec4ddddda8631e019b5737c8bc250
default_model_dir="$model_dir"
default_parent="$(dirname "$default_model_dir")"
hf_image="${HF_BOOTSTRAP_IMAGE:-python:3.12-slim}"

if [[ "${M5_MODEL_DIR:-}" == "" ]]; then
  if [[ ! -d "$default_parent" ]]; then
    if ! mkdir -p "$default_parent"; then
      fallback_root="${TMPDIR:-/tmp}/raglibrarian-model-cache"
      fallback_root="${fallback_root}-$(id -u)"
      rm -rf "$fallback_root"
      model_dir="$fallback_root/m5-jina-code-v1"
      M5_MODEL_DIR="$model_dir"
      mkdir -p "$fallback_root"
      chmod 700 "$fallback_root"
      echo "Unable to create default model cache parent $default_parent. Using fallback model dir: $model_dir"
    fi
  elif [[ ! -w "$default_parent" ]]; then
    fallback_root="${TMPDIR:-/tmp}/raglibrarian-model-cache-$(id -u)"
    model_dir="$fallback_root/m5-jina-code-v1"
    M5_MODEL_DIR="$model_dir"
    mkdir -p "$fallback_root"
    chmod 700 "$fallback_root"
    echo "Default model cache parent $default_parent is not writable. Using fallback model dir: $model_dir"
  fi
fi

if [[ "${M5_MODEL_DIR:-}" != "" ]]; then
  model_dir="$M5_MODEL_DIR"
fi
export M5_MODEL_DIR="${M5_MODEL_DIR:-$model_dir}"

normalize_model_permissions() {
  if [[ ! -w "$model_dir" || ! -O "$model_dir" ]]; then
    echo "Skipping model cache permission normalization: $model_dir is not writable by $(id -un)." >&2
    return
  fi
  local current_uid="$(id -u)"
  local warning=0
  find "$model_dir" \
    \( -path "$model_dir/.cache" -o -path "$model_dir/.cache/*" \) -prune \
    -o -type d -uid "$current_uid" -exec chmod 0755 {} + || {
      warning=1
    }
  find "$model_dir" \
    \( -path "$model_dir/.cache" -o -path "$model_dir/.cache/*" \) -prune \
    -o -type f -uid "$current_uid" -exec chmod 0444 {} + || {
      warning=1
    }
  if [[ "$warning" -ne 0 ]]; then
    echo "Warning: model cache permission normalization hit non-fatal filesystem restrictions; continuing." >&2
  fi
}

if [[ -d "$model_dir" && -f "$model_dir/.revision" ]] && [[ "$(cat "$model_dir/.revision")" == "$revision" ]]; then
  find "$model_dir" -type l -print -quit | grep -q . && { echo 'Model cache must not contain symlinks' >&2; exit 1; }
  find "$model_dir" -type f -name '*.safetensors' -print -quit | grep -q . || { echo 'Pinned safetensors are missing' >&2; exit 1; }
  [[ -f "$model_dir/onnx/model.onnx" ]] || { echo 'Pinned ONNX model is missing' >&2; exit 1; }
  [[ -f "$model_dir/config.json" ]] || { echo 'Pinned config file is missing' >&2; exit 1; }
  normalize_model_permissions
  exit 0
fi

if [[ -e "$model_dir" ]]; then
  if [[ "${M5_REPAIR_MODEL_CACHE:-false}" != "true" ]]; then
    echo "Refusing to replace incomplete model cache: $model_dir" >&2
    echo "Set M5_REPAIR_MODEL_CACHE=true to rebuild it." >&2
    exit 1
  fi
  if [[ ! -w "$(dirname "$model_dir")" ]]; then
    echo "Refusing to repair model cache: parent directory is not writable: $(dirname "$model_dir")" >&2
    echo "Update ownership/permissions or delete $model_dir manually before rerunning." >&2
    exit 1
  fi
  rm -rf "$model_dir" || {
    echo "Failed to replace incomplete model cache: $model_dir" >&2
    exit 1
  }
fi

run_hf_download() {
  local bootstrap_script
  bootstrap_script="$(mktemp)"
  trap 'rm -f "$bootstrap_script"' RETURN

  cat > "$bootstrap_script" <<'PY'
import os

from huggingface_hub import snapshot_download

snapshot_download(
    repo_id="jinaai/jina-embeddings-v2-base-code",
    revision=os.environ["HF_BOOTSTRAP_REVISION"],
    local_dir=os.environ["HF_BOOTSTRAP_MODEL_DIR"],
    local_dir_use_symlinks=False,
    allow_patterns=[
        "config.json",
        "tokenizer.json",
        "tokenizer_config.json",
        "special_tokens_map.json",
        "config_sentence_transformers.json",
        "preprocessor_config.json",
        "onnx/model.onnx",
        "*.safetensors",
        "*.txt",
    ],
)
PY

  if command -v python3 >/dev/null; then
    if python3 - <<'PY'
import importlib

importlib.import_module("huggingface_hub")
PY
    then
      if HF_BOOTSTRAP_REVISION="$revision" HF_BOOTSTRAP_MODEL_DIR="$model_dir" python3 "$bootstrap_script"; then
        echo "Using local Python snapshot_download to bootstrap the M5 model cache."
        return
      fi
      echo "Local Python snapshot_download failed. Falling back to Docker bootstrap."
    else
      echo "Could not import huggingface_hub into the local Python path."
    fi

    if command -v pip3 >/dev/null; then
      echo "Attempting to install huggingface_hub into the local Python user site."
      python3 -m pip install --user --disable-pip-version-check --no-input --no-deps "huggingface_hub>=0.29" || true
      if python3 - <<'PY'
import importlib

importlib.import_module("huggingface_hub")
PY
      then
        if HF_BOOTSTRAP_REVISION="$revision" HF_BOOTSTRAP_MODEL_DIR="$model_dir" python3 "$bootstrap_script"; then
          echo "Using local Python snapshot_download to bootstrap the M5 model cache."
          return
        fi
        echo "Installed huggingface_hub still cannot execute snapshot_download. Falling back to Docker bootstrap."
      fi
    fi
  fi

  echo "Local snapshot_download path is unavailable. Falling back to Docker bootstrap."

  command -v docker >/dev/null || {
    echo 'Neither hf CLI nor docker is available to bootstrap the M5 model cache.' >&2
    echo 'Install one of the following and retry:' >&2
    echo '  - pipx install huggingface_hub' >&2
    echo '  - python3 -m pip install --user "huggingface_hub[cli]"' >&2
    echo '  - docker (to run a temporary bootstrap container)' >&2
    exit 1
  }

  echo 'No local snapshot_download path found; using Docker to bootstrap the M5 model cache.'
  cat "$bootstrap_script" | docker run --rm -i \
    --user "$(id -u):$(id -g)" \
    --mount type=bind,src="$(readlink -f "$model_dir")",dst=/data/model \
    -e HF_BOOTSTRAP_REVISION="$revision" \
    -e HF_BOOTSTRAP_MODEL_DIR=/data/model \
    -e HF_HUB_CACHE=/tmp/huggingface \
    -e HF_HOME=/tmp/huggingface_home \
    -e XDG_CACHE_HOME=/tmp/huggingface_home \
    -e HOME=/tmp \
    "$hf_image" \
    bash -lc 'mkdir -p "$HF_HUB_CACHE" "$HF_HOME" && \
      python -m pip install --no-cache-dir --user --no-deps "huggingface_hub" >/tmp/huggingface_hub_install.log && \
      cat > /tmp/bootstrap-hf.py && \
      python /tmp/bootstrap-hf.py'
}

mkdir -p "$model_dir"
chmod 700 "$model_dir"
rm -rf "$model_dir/.cache" "$model_dir/.cache/huggingface" 2>/dev/null || true
run_hf_download
find "$model_dir" -type l -print -quit | grep -q . && { echo 'Downloaded model cache contains symlinks' >&2; exit 1; }
[[ -f "$model_dir/config.json" ]] || { echo 'Pinned config file was not downloaded' >&2; exit 1; }
find "$model_dir" -type f -name '*.safetensors' -print -quit | grep -q . || { echo 'Pinned safetensors were not downloaded' >&2; exit 1; }
[[ -f "$model_dir/onnx/model.onnx" ]] || { echo 'Pinned ONNX model was not downloaded' >&2; exit 1; }
printf '%s\n' "$revision" > "$model_dir/.revision"
normalize_model_permissions

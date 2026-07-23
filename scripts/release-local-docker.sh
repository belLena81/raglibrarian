#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

: "${ANSWER_LLM_BASE_URL:?set ANSWER_LLM_BASE_URL to the local HTTPS OpenAI-compatible provider}"
: "${ANSWER_LLM_MODEL:?set ANSWER_LLM_MODEL}"
: "${ANSWER_LLM_API_KEY_PATH:=.dev/secrets/answer_llm_api_key}"

case "$ANSWER_LLM_BASE_URL" in
  https://llm-provider-stub|https://llm-provider-stub:*|https://llm-provider-stub/*)
    echo "release local Docker stage requires a non-stub provider" >&2
    exit 2
    ;;
  https://*) ;;
  *)
    echo "release local Docker stage requires an HTTPS provider" >&2
    exit 2
    ;;
esac

[[ -f "$ANSWER_LLM_API_KEY_PATH" && ! -L "$ANSWER_LLM_API_KEY_PATH" && -r "$ANSWER_LLM_API_KEY_PATH" ]] || {
  echo "ANSWER_LLM_API_KEY_PATH must be a readable regular file" >&2
  exit 2
}

if [[ -n "${RELEASE_LOCAL_PROVIDER_START_COMMAND:-}" ]]; then
  "$RELEASE_LOCAL_PROVIDER_START_COMMAND"
fi

make dev-secrets
make m6-stack-up
make release-local-test

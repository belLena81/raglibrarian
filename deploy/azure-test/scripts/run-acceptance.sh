#!/usr/bin/env bash
set -euo pipefail

: "${RAGLIBRARIAN_ACCEPTANCE_COMMAND:?set RAGLIBRARIAN_ACCEPTANCE_COMMAND to the private runner test command}"
: "${RAGLIBRARIAN_ACCEPTANCE_MODE:?set RAGLIBRARIAN_ACCEPTANCE_MODE to worker or serverless}"
: "${GITHUB_SHA:?set GITHUB_SHA to the workflow commit under test}"

case "$RAGLIBRARIAN_ACCEPTANCE_MODE" in
  worker|serverless) ;;
  *) echo "RAGLIBRARIAN_ACCEPTANCE_MODE must be worker or serverless" >&2; exit 2 ;;
esac

# The protected runner owns test credentials and writes them only to owner-only
# temporary files. This repository intentionally does not know their values.
evidence_file=$(mktemp /tmp/raglibrarian-acceptance.XXXXXX.json)
chmod 600 "$evidence_file"
trap 'rm -f "$evidence_file"' EXIT

export RAGLIBRARIAN_ACCEPTANCE_EVIDENCE_FILE="$evidence_file"
"$RAGLIBRARIAN_ACCEPTANCE_COMMAND" m4 m6 m7
deploy/azure-test/scripts/validate-acceptance-evidence.sh "$evidence_file" "$RAGLIBRARIAN_ACCEPTANCE_MODE" "$GITHUB_SHA"

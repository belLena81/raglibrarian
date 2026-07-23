#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)
cd "$repo_root"

: "${RAGLIBRARIAN_WORKER_ACCEPTANCE_COMMAND:?set RAGLIBRARIAN_WORKER_ACCEPTANCE_COMMAND}"

evidence_file=$(mktemp /tmp/raglibrarian-worker-evidence.XXXXXX.json)
chmod 600 "$evidence_file"
trap 'rm -f "$evidence_file"' EXIT

bash deploy/cloud-test/scripts/validate.sh
bash deploy/cloud-test/scripts/start.sh

export RAGLIBRARIAN_ACCEPTANCE_EVIDENCE_FILE="$evidence_file"
"$RAGLIBRARIAN_WORKER_ACCEPTANCE_COMMAND" m4 m6 m7

deploy/azure-test/scripts/validate-acceptance-evidence.sh "$evidence_file" worker

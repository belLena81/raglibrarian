#!/usr/bin/env bash
set -euo pipefail

compose_file="${1:-docker-compose.yml}"
template="${2:-infra/aws/m5/template.yaml}"
[[ -f "$compose_file" && -f "$template" ]] || { echo 'M5 runtime files are missing' >&2; exit 1; }

# Compose is the Lambda substitute, so it must expose one worker command and no
# Lambda preparation command. Search/TEI/Qdrant are deliberately not consumers.
[[ "$(grep -c '^  retrieval-worker:$' "$compose_file")" == 1 ]] || { echo 'Compose must define exactly one retrieval-worker' >&2; exit 1; }
grep -A35 '^  retrieval-worker:$' "$compose_file" | grep -Fq 'RETRIEVAL_PROCESSING_MODE: worker' || { echo 'Compose Retrieval worker mode is not frozen' >&2; exit 1; }
if grep -Eq '^  retrieval-(planner|index|dispatcher|cleanup)-lambda:' "$compose_file"; then
  echo 'Compose must not define M5 Lambda preparation services' >&2
  exit 1
fi

grep -Fq '  LambdaMode:' "$template" || { echo 'M5 template is missing LambdaMode' >&2; exit 1; }
for resource in PlannerFunction BookUploadedMapping ChunksReadyMapping IndexFunction IndexBatchMapping DispatcherFunction CleanupFunction; do
  block=$(awk -v resource="$resource" '
    $0 == "  " resource ":" { capture=1 }
    capture && $0 ~ /^  [A-Za-z0-9]+:/ && $0 != "  " resource ":" { exit }
    capture { print }
  ' "$template")
  printf '%s\n' "$block" | grep -Fq 'Condition: LambdaMode' || { echo "$resource must be LambdaMode-only" >&2; exit 1; }
done
if grep -Fq 'AWS::ECS::Service' "$template"; then
  echo 'AWS M5 stack must not deploy the local substitute worker' >&2
  exit 1
fi
grep -Fq 'AllowedValues: [lambda, paused]' "$template" || { echo 'ProcessingMode is not closed' >&2; exit 1; }

echo 'M5 preparation modes are exclusive; Compose is worker-only'

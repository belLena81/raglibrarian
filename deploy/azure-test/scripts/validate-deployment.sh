#!/usr/bin/env bash
set -euo pipefail

parameters_file=${1:?usage: validate-deployment.sh PARAMETER_FILE}
: "${AZURE_ACR_LOGIN_SERVER:?set AZURE_ACR_LOGIN_SERVER}"
: "${AZURE_ALLOWED_IMAGE_REPOSITORIES:?set AZURE_ALLOWED_IMAGE_REPOSITORIES}"
: "${AZURE_VERIFY_APPROVED_IMAGE_COMMAND:?set AZURE_VERIFY_APPROVED_IMAGE_COMMAND}"

command -v az >/dev/null
command -v jq >/dev/null
test -r "$parameters_file"

jq -e --arg registry "${AZURE_ACR_LOGIN_SERVER%/}" '
  .parameters.processingMode.value as $mode |
  .parameters.runtimeEnvironments.value as $runtime |
  ($mode == "paused" or $mode == "serverless") and
  (.parameters.images.value | to_entries | length >= 3) and
  ([.parameters.images.value[] | strings] | all(startswith($registry + "/") and test("@sha256:[0-9a-f]{64}$"))) and
  (.parameters.secretVersions.value | to_entries | length == 25) and
  ([.parameters.secretVersions.value[] | strings] | all(test("^[0-9A-Fa-f]{32}$"))) and
  ([$runtime.planner, $runtime.index] | all(
    (.RETRIEVAL_QDRANT_URL | strings | test("^http://[^/]+:6333$")) and
    (.RETRIEVAL_TEI_URL | strings | test("^http://[^/]+:8080$")) and
    (.RETRIEVAL_METRICS_ADDR | strings | test("^[^:]+:[0-9]+$"))
  )) and
  ($runtime.ingestionCleanup | (
    (.INGESTION_MINIO_ENDPOINT | strings | test("^[^/:]+:[0-9]+$")) and
    (.INGESTION_MINIO_INSECURE == "false") and
    (.INGESTION_ARTIFACT_BUCKET | strings | length > 0) and
    (.INGESTION_CLEANUP_INTERVAL == "15m")
  )) and
  ($runtime.retrievalCleanup.RETRIEVAL_QDRANT_URL | strings | test("^http://[^/]+:6333$"))
' "$parameters_file" >/dev/null

while IFS= read -r image; do
  repository=${image#"${AZURE_ACR_LOGIN_SERVER%/}"/}
  repository=${repository%@sha256:*}
  case ",${AZURE_ALLOWED_IMAGE_REPOSITORIES}," in
    *",${repository},"*) ;;
    *) echo "Image repository is not allowlisted: $repository" >&2; exit 1 ;;
  esac
  "$AZURE_VERIFY_APPROVED_IMAGE_COMMAND" "$image"
done < <(jq -r '.parameters.images.value[]' "$parameters_file")

az bicep build --file infra/azure-test/main.bicep --stdout >/dev/null
echo 'Azure test deployment input is structurally valid; no secret values were read.'

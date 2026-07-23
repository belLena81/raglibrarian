#!/usr/bin/env bash
set -euo pipefail

: "${AZURE_RESOURCE_GROUP:?set AZURE_RESOURCE_GROUP}"
: "${AZURE_DEPLOYMENT_NAME:?set AZURE_DEPLOYMENT_NAME}"
: "${AZURE_PARAMETERS_FILE:?set AZURE_PARAMETERS_FILE}"
: "${AZURE_PROCESSING_MODE:?set AZURE_PROCESSING_MODE to paused or serverless}"
: "${AZURE_VERIFY_CONSUMERS_COMMAND:?set AZURE_VERIFY_CONSUMERS_COMMAND to the private runner verifier}"

case "$AZURE_PROCESSING_MODE" in
  paused|serverless) ;;
  *) echo 'AZURE_PROCESSING_MODE must be paused or serverless.' >&2; exit 2 ;;
esac

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/../../.." && pwd)
cd "$repo_root"

"$script_dir/validate-deployment.sh" "$AZURE_PARAMETERS_FILE"

wait_for_scheduled_executions() {
  wait_for_active_executions Schedule
}

wait_for_active_executions() {
  local trigger_type=$1 job attempt active
  while IFS= read -r job; do
    [ -n "$job" ] || continue
    active=0
    for attempt in $(seq 1 24); do
      active=$(az containerapp job execution list --resource-group "$AZURE_RESOURCE_GROUP" --name "$job" \
        --query "length([?properties.status=='Running' || properties.status=='Pending'])" --output tsv --only-show-errors)
      [ "$active" = 0 ] && break
      sleep 5
    done
    [ "$active" = 0 ] || { echo "Scheduled job $job did not quiesce." >&2; return 1; }
  done < <(az containerapp job list --resource-group "$AZURE_RESOURCE_GROUP" \
    --query "[?tags.application=='raglibrarian' && tags.environment=='$(jq -r '.parameters.environmentName.value' "$AZURE_PARAMETERS_FILE")' && properties.configuration.triggerType=='$trigger_type'].name" \
    --output tsv --only-show-errors)
}

stop_active_executions() {
  local trigger_type=$1 job execution
  while IFS= read -r job; do
    [ -n "$job" ] || continue
    while IFS= read -r execution; do
      [ -n "$execution" ] || continue
      az containerapp job stop --resource-group "$AZURE_RESOURCE_GROUP" --name "$job" \
        --job-execution-name "$execution" --only-show-errors
    done < <(az containerapp job execution list --resource-group "$AZURE_RESOURCE_GROUP" --name "$job" \
      --query "[?properties.status=='Running' || properties.status=='Pending'].name" --output tsv --only-show-errors)
  done < <(az containerapp job list --resource-group "$AZURE_RESOURCE_GROUP" \
    --query "[?tags.application=='raglibrarian' && tags.environment=='$(jq -r '.parameters.environmentName.value' "$AZURE_PARAMETERS_FILE")' && properties.configuration.triggerType=='$trigger_type'].name" \
    --output tsv --only-show-errors)
  wait_for_active_executions "$trigger_type"
}

delete_event_jobs() {
  local job execution
  while IFS= read -r job; do
    [ -n "$job" ] || continue
    while IFS= read -r execution; do
      [ -n "$execution" ] || continue
      az containerapp job stop --resource-group "$AZURE_RESOURCE_GROUP" --name "$job" \
        --job-execution-name "$execution" --only-show-errors
    done < <(az containerapp job execution list --resource-group "$AZURE_RESOURCE_GROUP" --name "$job" \
      --query "[?properties.status=='Running' || properties.status=='Pending'].name" --output tsv --only-show-errors)
  done < <(az containerapp job list --resource-group "$AZURE_RESOURCE_GROUP" \
    --query "[?tags.application=='raglibrarian' && tags.environment=='$(jq -r '.parameters.environmentName.value' "$AZURE_PARAMETERS_FILE")' && properties.configuration.triggerType=='Event'].name" \
    --output tsv --only-show-errors)
  wait_for_active_executions Event
  while IFS= read -r job; do
    [ -n "$job" ] || continue
    az containerapp job delete --resource-group "$AZURE_RESOURCE_GROUP" --name "$job" --yes --only-show-errors
  done < <(az containerapp job list --resource-group "$AZURE_RESOURCE_GROUP" \
    --query "[?tags.application=='raglibrarian' && tags.environment=='$(jq -r '.parameters.environmentName.value' "$AZURE_PARAMETERS_FILE")' && properties.configuration.triggerType=='Event'].name" \
    --output tsv --only-show-errors)
}

delete_scheduled_jobs() {
  local job execution
  # Stop scheduling by deleting the schedule resources. ARM incremental mode
  # retains omitted resources, so this must precede the paused deployment.
  stop_active_executions Schedule
  while IFS= read -r job; do
    [ -n "$job" ] || continue
    while IFS= read -r execution; do
      [ -n "$execution" ] || continue
      az containerapp job stop --resource-group "$AZURE_RESOURCE_GROUP" --name "$job" \
        --job-execution-name "$execution" --only-show-errors
    done < <(az containerapp job execution list --resource-group "$AZURE_RESOURCE_GROUP" --name "$job" \
      --query "[?properties.status=='Running' || properties.status=='Pending'].name" --output tsv --only-show-errors)
  done < <(az containerapp job list --resource-group "$AZURE_RESOURCE_GROUP" \
    --query "[?tags.application=='raglibrarian' && tags.environment=='$(jq -r '.parameters.environmentName.value' "$AZURE_PARAMETERS_FILE")' && properties.configuration.triggerType=='Schedule'].name" \
    --output tsv --only-show-errors)
  wait_for_scheduled_executions
  while IFS= read -r job; do
    [ -n "$job" ] || continue
    az containerapp job delete --resource-group "$AZURE_RESOURCE_GROUP" --name "$job" --yes --only-show-errors
  done < <(az containerapp job list --resource-group "$AZURE_RESOURCE_GROUP" \
    --query "[?tags.application=='raglibrarian' && tags.environment=='$(jq -r '.parameters.environmentName.value' "$AZURE_PARAMETERS_FILE")' && properties.configuration.triggerType=='Schedule'].name" \
    --output tsv --only-show-errors)
}

if [ "$AZURE_PROCESSING_MODE" = paused ]; then
  stop_active_executions Event
  delete_event_jobs
  delete_scheduled_jobs
fi

az deployment group create \
  --resource-group "$AZURE_RESOURCE_GROUP" \
  --name "$AZURE_DEPLOYMENT_NAME" \
  --template-file infra/azure-test/main.bicep \
  --parameters "@$AZURE_PARAMETERS_FILE" processingMode="$AZURE_PROCESSING_MODE" \
  --only-show-errors >/dev/null

event_job_count() {
  az containerapp job list --resource-group "$AZURE_RESOURCE_GROUP" \
    --query "length([?tags.application=='raglibrarian' && tags.environment=='$(jq -r '.parameters.environmentName.value' "$AZURE_PARAMETERS_FILE")' && properties.configuration.triggerType=='Event'])" \
    --output tsv --only-show-errors
}

verify_paused_jobs() {
  local expected=$1 actual
  actual=$(event_job_count)
  test "$actual" = "$expected" || {
    echo "Expected $expected event jobs while paused, but found $actual." >&2
    return 1
  }
}

verify_enabled_jobs() {
  local job expected actual
  while IFS= read -r job; do
    case "$job" in
      *-ingestion) expected=2 ;;
      *) expected=4 ;;
    esac
    actual=$(az containerapp job show --resource-group "$AZURE_RESOURCE_GROUP" --name "$job" \
      --query 'properties.configuration.eventTriggerConfig.scale.maxExecutions' --output tsv --only-show-errors)
    test "$actual" = "$expected" || {
      echo "Job $job has maxExecutions=$actual; expected $expected." >&2
      return 1
    }
  done < <(az containerapp job list --resource-group "$AZURE_RESOURCE_GROUP" \
    --query "[?tags.application=='raglibrarian' && tags.environment=='$(jq -r '.parameters.environmentName.value' "$AZURE_PARAMETERS_FILE")' && properties.configuration.triggerType=='Event'].name" \
    --output tsv --only-show-errors)
}

scheduled_job_count() {
  az containerapp job list --resource-group "$AZURE_RESOURCE_GROUP" \
    --query "length([?tags.application=='raglibrarian' && tags.environment=='$(jq -r '.parameters.environmentName.value' "$AZURE_PARAMETERS_FILE")' && properties.configuration.triggerType=='Schedule'])" \
    --output tsv --only-show-errors
}

if [ "$AZURE_PROCESSING_MODE" = paused ]; then
  verify_paused_jobs 0
  wait_for_active_executions Event
  test "$(scheduled_job_count)" = 0 || { echo 'Scheduled jobs remain while paused.' >&2; exit 1; }
  "$AZURE_VERIFY_CONSUMERS_COMMAND" zero
  echo 'Serverless jobs are paused and the private broker reports zero consumers.'
else
  "$AZURE_VERIFY_CONSUMERS_COMMAND" zero
  wait_for_active_executions Event
  verify_enabled_jobs
  test "$(scheduled_job_count)" = 4 || { echo 'Expected four enabled scheduled jobs.' >&2; exit 1; }
  echo 'Serverless jobs are enabled; worker consumers must remain stopped.'
fi

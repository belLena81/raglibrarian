#!/usr/bin/env bash
set -euo pipefail

compose_cmd() {
  M5_MODEL_DIR="$M5_MODEL_DIR" docker compose --profile raglibrarian "$@"
}

compose_service_id() {
  local service="$1"

  compose_cmd ps --all -q "$service" 2>/dev/null || true
}

wait_for_service_healthy() {
  local service="$1"
  local timeout="${2:-$compose_wait_timeout}"
  local label="${3:-$service}"
  local container_id status health exit_code elapsed

  for elapsed in $(seq 1 "$timeout"); do
    container_id="$(compose_service_id "$service")"
    if [[ -z "$container_id" ]]; then
      sleep 1
      continue
    fi

    status="$(docker inspect --format '{{.State.Status}}' "$container_id" 2>/dev/null || true)"
    health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{end}}' "$container_id" 2>/dev/null || true)"
    exit_code="$(docker inspect --format '{{.State.ExitCode}}' "$container_id" 2>/dev/null || true)"

    if [[ "$health" == "healthy" ]]; then
      return 0
    fi
    if [[ "$status" == "exited" ]]; then
      echo "$label exited before becoming healthy (exit code ${exit_code:-unknown})."
      return 1
    fi

    if (( elapsed % 10 == 0 )); then
      echo "Waiting for $label to become healthy: ${elapsed}s/${timeout}s"
    fi
    sleep 1
  done

  echo "Timed out waiting for $label to become healthy after ${timeout}s."
  return 1
}

wait_for_service_completed_successfully() {
  local service="$1"
  local timeout="${2:-$compose_wait_timeout}"
  local label="${3:-$service}"
  local container_id status exit_code elapsed

  for elapsed in $(seq 1 "$timeout"); do
    container_id="$(compose_service_id "$service")"
    if [[ -z "$container_id" ]]; then
      sleep 1
      continue
    fi

    status="$(docker inspect --format '{{.State.Status}}' "$container_id" 2>/dev/null || true)"
    exit_code="$(docker inspect --format '{{.State.ExitCode}}' "$container_id" 2>/dev/null || true)"

    if [[ "$status" == "exited" ]]; then
      if [[ "${exit_code:-1}" == "0" ]]; then
        return 0
      fi
      echo "$label failed with exit code ${exit_code:-unknown}."
      docker compose --profile raglibrarian logs --no-color --tail 120 "$service" 2>/dev/null || true
      return 1
    fi

    if (( elapsed % 10 == 0 )); then
      echo "Waiting for $label to complete successfully: ${elapsed}s/${timeout}s"
    fi
    sleep 1
  done

  echo "Timed out waiting for $label to complete successfully after ${timeout}s."
  return 1
}

print_tei_debug() {
  local tei_container_id
  tei_container_id="$(docker compose --profile raglibrarian ps -q text-embeddings-inference || true)"
  if [[ -z "$tei_container_id" ]]; then
    echo "No text-embeddings-inference container was found yet."
    return
  fi
  echo "---- text-embeddings-inference runtime state ----"
  docker inspect --format 'id={{.Id}} state={{.State.Status}} exit={{.State.ExitCode}} oom_killed={{.State.OOMKilled}} error={{.State.Error}} started={{.State.StartedAt}} finished={{.State.FinishedAt}}' "$tei_container_id" || true
  tei_health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$tei_container_id" 2>/dev/null || true)"
  echo "TEI health status: ${tei_health:-unknown}"
  echo "---- recent TEI health output ----"
  docker inspect --format '{{with .State.Health}}{{range .Log}}{{printf "%s %s\n" .End .Output}}{{end}}{{end}}' "$tei_container_id" | tail -n 8 || true
}

report_dependency_status() {
  echo "---- compose status ----"
  docker compose --profile raglibrarian ps || true
  echo "---- tei state ----"
  docker compose --profile raglibrarian ps text-embeddings-inference || true
  echo "---- edge-api state ----"
  docker compose --profile raglibrarian ps edge-api || true
  if docker compose --profile raglibrarian ps text-embeddings-inference | tail -n +3 | grep -q "health: starting"; then
    echo "text-embeddings-inference is still warming up."
  fi
  if docker compose --profile raglibrarian ps text-embeddings-inference | tail -n +3 | grep -Eq "Restarting|Restarted|Exit|Exited|unhealthy|starting"; then
    echo "Text-embeddings-inference appears unhealthy/restarting. Last logs:"
    docker compose --profile raglibrarian logs --no-color --tail 160 text-embeddings-inference 2>/dev/null || true
    print_tei_debug
  fi
}

check_tei_failure_exit() {
  local tei_container_id tei_status tei_exit_code

  tei_container_id="$(docker compose --profile raglibrarian ps -q text-embeddings-inference || true)"
  if [[ -z "$tei_container_id" ]]; then
    return 1
  fi

  tei_status="$(docker inspect --format '{{.State.Status}}' "$tei_container_id" 2>/dev/null || true)"
  tei_exit_code="$(docker inspect --format '{{.State.ExitCode}}' "$tei_container_id" 2>/dev/null || true)"
  if [[ "$tei_status" == "exited" && "${tei_exit_code:-0}" != "0" ]]; then
    echo "text-embeddings-inference exited during startup (exit code ${tei_exit_code})."
    echo "Recent logs:"
    docker compose --profile raglibrarian logs --no-color --tail 200 text-embeddings-inference 2>/dev/null || true
    return 0
  fi
  return 1
}

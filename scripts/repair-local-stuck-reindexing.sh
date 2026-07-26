#!/usr/bin/env bash

set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

resolve_dsn() {
  local preferred_file="$1"
  local fallback_file="$2"
  local fallback_host="$3"

  if [[ -f "$preferred_file" ]]; then
    tr -d '\n' < "$preferred_file"
    return
  fi
  if [[ -f "$fallback_file" ]]; then
    sed "s/@postgres:/@${fallback_host}:/g" "$fallback_file" | tr -d '\n'
    return
  fi
  return 1
}

retrieval_dsn="$(
  resolve_dsn \
    "$root_dir/.dev/host-secrets/retrieval_runtime_dsn" \
    "$root_dir/.dev/secrets/retrieval_runtime_host_dsn" \
    "127.0.0.1"
)"

catalog_dsn="$(
  resolve_dsn \
    "$root_dir/.dev/host-secrets/catalog_runtime_dsn" \
    "$root_dir/.dev/secrets/catalog_runtime_dsn" \
    "127.0.0.1"
)"

if [[ "$retrieval_dsn" == *"@postgres:"* ]]; then
  retrieval_dsn="$(printf '%s' "$retrieval_dsn" | sed 's/@postgres:/@127.0.0.1:/g')"
fi

if [[ "$catalog_dsn" == *"@postgres:"* ]]; then
  catalog_dsn="$(printf '%s' "$catalog_dsn" | sed 's/@postgres:/@127.0.0.1:/g')"
fi

repair_candidates_sql="
WITH failed_reindexes AS (
    SELECT
        lifecycle.book_id,
        lifecycle.lifecycle_version,
        failed_job.failure_category,
        (
            SELECT previous_job.id
            FROM retrieval.index_jobs AS previous_job
            WHERE previous_job.book_id = lifecycle.book_id
              AND previous_job.state = 'indexed'
              AND previous_job.lifecycle_version < lifecycle.lifecycle_version
            ORDER BY previous_job.lifecycle_version DESC, previous_job.updated_at DESC, previous_job.created_at DESC
            LIMIT 1
        ) AS fallback_job_id
    FROM retrieval.book_lifecycle AS lifecycle
    JOIN retrieval.index_jobs AS failed_job
      ON failed_job.book_id = lifecycle.book_id
     AND failed_job.lifecycle_version = lifecycle.lifecycle_version
     AND failed_job.state = 'failed'
    WHERE lifecycle.state = 'reindexing'
      AND lifecycle.active_job_id IS NULL
)
SELECT book_id, lifecycle_version, fallback_job_id, failure_category
FROM failed_reindexes
WHERE fallback_job_id IS NOT NULL
ORDER BY book_id;
"

candidate_lines="$(
  psql "$retrieval_dsn" -X -A -F '|' -t -c "$repair_candidates_sql"
)"

if [[ -z "$candidate_lines" ]]; then
  echo "No stuck reindexing rows found."
  exit 0
fi

while IFS='|' read -r book_id lifecycle_version fallback_job_id failure_category; do
  [[ -n "$book_id" ]] || continue

  psql "$retrieval_dsn" -X -v ON_ERROR_STOP=1 \
    -v book_id="$book_id" \
    -v lifecycle_version="$lifecycle_version" \
    -v fallback_job_id="$fallback_job_id" <<'SQL'
UPDATE retrieval.book_lifecycle
SET state='active',
    active_job_id=:'fallback_job_id',
    cleanup_pending=false,
    updated_at=now()
WHERE book_id=:'book_id'
  AND lifecycle_version=:'lifecycle_version'
  AND state='reindexing'
  AND active_job_id IS NULL;
SQL

  psql "$catalog_dsn" -X -v ON_ERROR_STOP=1 \
    -v book_id="$book_id" \
    -v failure_category="$failure_category" <<'SQL'
UPDATE catalog.books
SET processing_status='failed',
    processing_stage='failed',
    processing_failure_category=:'failure_category',
    processing_updated_at=now(),
    processing_version=processing_version+1
WHERE id=:'book_id'
  AND processing_status='reindexing'
  AND processing_stage='chunks_ready';
SQL

  echo "repaired book_id=$book_id lifecycle_version=$lifecycle_version fallback_job_id=$fallback_job_id failure_category=$failure_category"
done <<< "$candidate_lines"

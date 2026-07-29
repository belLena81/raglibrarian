#!/usr/bin/env bash
set -euo pipefail

COLLECTION="${COLLECTION:-evidence_v2}"
QDRANT_CONTAINER="${QDRANT_CONTAINER:-raglibrarian-qdrant-1}"
QDRANT_URL="${QDRANT_URL:-http://127.0.0.1:6333}"
CURL_IMAGE="${CURL_IMAGE:-curlimages/curl:8.11.0}"
QUERY_LIMIT="${QUERY_LIMIT:-3}"
DOCUMENT_LIMIT="${DOCUMENT_LIMIT:-2}"
SCORE_THRESHOLD="${SCORE_THRESHOLD:-0}"
QUESTION_VECTOR_DIMENSIONS="${QUESTION_VECTOR_DIMENSIONS:-768}"
INDEXED_ONLY="${INDEXED_ONLY:-true}"
SHOW_RAW_PAYLOAD="${SHOW_RAW_PAYLOAD:-false}"

# Scope filters (optional):
# BOOK_ID   -> filter source document lookup by book_id
# JOB_ID    -> bypass document lookup and hydrate one job directly
# JOB_IDS   -> JSON array of job ids, e.g. ["job-1","job-2"]
#
# Query vector selector:
# QUESTION              -> embed through TEI (normal mode)
# QUESTION_VECTOR_JSON  -> precomputed vector override
#
# Examples:
#   QUESTION='vim registers' ./scripts/check-qdrant-query-batch.sh
#   QUESTION='vim registers' JOB_ID=job-123 ./scripts/check-qdrant-query-batch.sh
#   QUESTION_VECTOR_JSON='[0,0,...]' JOB_IDS='["job-1","job-2"]' ./scripts/check-qdrant-query-batch.sh
#
BOOK_ID="${BOOK_ID:-}"
JOB_ID="${JOB_ID:-}"
JOB_IDS="${JOB_IDS:-}"
QUESTION="${QUESTION:-}"
QUESTION_VECTOR_JSON="${QUESTION_VECTOR_JSON:-}"
TEI_URL="${TEI_URL:-${RETRIEVAL_TEI_URL:-}}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
REPO_ROOT="$(dirname "$SCRIPT_DIR")"

if ! command -v jq >/dev/null 2>&1; then
    echo "jq is required. Install it first." >&2
    exit 1
fi

get_api_key() {
    if [[ -n "${QDRANT_API_KEY:-}" ]]; then
        printf '%s' "$QDRANT_API_KEY"
        return
    fi

    if [[ -r "${REPO_ROOT}/.dev/secrets/retrieval_qdrant_read_api_key" ]]; then
        tr -d '\r\n' < "${REPO_ROOT}/.dev/secrets/retrieval_qdrant_read_api_key"
        return
    fi

    docker exec "$QDRANT_CONTAINER" sh -lc 'cat /run/secrets/retrieval_qdrant_read_api_key 2>/dev/null || true' | tr -d '\r\n'
}

load_host_services_tei_url() {
    local env_file="${REPO_ROOT}/.dev/host-services.env"
    if [[ -r "$env_file" ]]; then
        sed -n 's/^export RETRIEVAL_TEI_URL="\([^"]*\)"$/\1/p' "$env_file" | head -n 1
    fi
}

resolve_tei_url() {
    if [[ -n "$TEI_URL" ]]; then
        printf '%s' "$TEI_URL"
        return
    fi

    local from_env
    from_env="$(load_host_services_tei_url)"
    if [[ -n "$from_env" ]]; then
        printf '%s' "$from_env"
        return
    fi
}

API_KEY="$(get_api_key)"
if [[ -z "${API_KEY}" ]]; then
    echo "No Qdrant read API key found. Set QDRANT_API_KEY or ensure a readable secret exists." >&2
    exit 1
fi

run_container_http() {
    local method="$1"
    local url="$2"
    local payload="${3:-}"
    shift 3 || true
    local extra_args=("$@")

    if [[ -n "$payload" ]]; then
        docker run --rm --network "container:${QDRANT_CONTAINER}" \
            "$CURL_IMAGE" -sS -i \
            "${extra_args[@]}" \
            -H "Content-Type: application/json" \
            -X "$method" \
            -d "$payload" \
            "$url"
    else
        docker run --rm --network "container:${QDRANT_CONTAINER}" \
            "$CURL_IMAGE" -sS -i \
            "${extra_args[@]}" \
            "$url"
    fi
}

run_qdrant() {
    local method="$1"
    local path="$2"
    local payload="${3:-}"
    run_container_http "$method" "${QDRANT_URL}${path}" "$payload" -H "api-key: ${API_KEY}"
}

extract_body() {
    awk 'BEGIN { body = 0 } body { print } /^\r?$/ { body = 1 }'
}

extract_status() {
    sed -n '1s/.* \([0-9][0-9][0-9]\) .*/\1/p'
}

validate_vector_json() {
    jq -ce --argjson dimensions "$QUESTION_VECTOR_DIMENSIONS" '
        if type != "array" then
            error("vector must be a JSON array")
        elif length != $dimensions then
            error("vector dimensions mismatch")
        elif all(.[]; type == "number") then
            .
        else
            error("vector entries must be numbers")
        end
    ' <<< "$1"
}

embed_question_vector() {
    local resolved_tei_url payload response status body
    resolved_tei_url="$(resolve_tei_url)"
    if [[ -z "$resolved_tei_url" ]]; then
        echo "TEI_URL is required when QUESTION_VECTOR_JSON is not provided. Set TEI_URL, RETRIEVAL_TEI_URL, or .dev/host-services.env." >&2
        exit 1
    fi
    if [[ -z "$QUESTION" ]]; then
        echo "QUESTION is required when QUESTION_VECTOR_JSON is not provided." >&2
        exit 1
    fi

    payload="$(jq -nc --arg question "Represent this sentence for searching relevant passages: ${QUESTION}" '{inputs: $question, truncate: true}')"
    response="$(run_container_http "POST" "${resolved_tei_url}/embed" "$payload")"
    status="$(printf '%s\n' "$response" | extract_status)"
    body="$(printf '%s\n' "$response" | extract_body)"

    if [[ "$status" != "200" ]]; then
        echo "TEI query embedding failed with HTTP ${status:-unknown}." >&2
        printf '%s\n' "$response" >&2
        exit 1
    fi

    jq -ce --argjson dimensions "$QUESTION_VECTOR_DIMENSIONS" '
        if type != "array" or length != 1 or (.[0] | type) != "array" then
            error("invalid TEI embedding response shape")
        elif (.[0] | length) != $dimensions then
            error("invalid TEI embedding dimensions")
        elif (.[0] | all(.[]; type == "number")) then
            .[0]
        else
            error("invalid TEI embedding values")
        end
    ' <<< "$body"
}

resolve_vector() {
    if [[ -n "$QUESTION_VECTOR_JSON" ]]; then
        validate_vector_json "$QUESTION_VECTOR_JSON"
        return
    fi

    embed_question_vector
}

build_document_filter() {
    local filter='{"must":[]}'

    if [[ "$INDEXED_ONLY" == "true" ]]; then
        filter="$(jq -c '.must += [{ "key":"indexed", "match":{"value":"true"} }]' <<< "$filter")"
    fi

    filter="$(jq -c '.must += [{ "key":"vector_kind", "match":{"value":"document"} }]' <<< "$filter")"

    if [[ -n "$BOOK_ID" ]]; then
        filter="$(jq -c --arg book "$BOOK_ID" '.must += [{ "key":"book_id", "match":{"value":$book} }]' <<< "$filter")"
    fi

    if [[ -n "$JOB_ID" ]]; then
        filter="$(jq -c --arg job "$JOB_ID" '.must += [{ "key":"job_id", "match":{"value":$job} }]' <<< "$filter")"
    fi

    printf '%s' "$filter"
}

resolve_job_ids() {
    if [[ -n "$JOB_IDS" ]]; then
        jq -ce 'if type == "array" and all(.[]; type == "string" and length > 0) then . else error("JOB_IDS must be a JSON array of non-empty strings") end' <<< "$JOB_IDS"
        return
    fi

    if [[ -n "$JOB_ID" ]]; then
        jq -nc --arg job "$JOB_ID" '[$job]'
        return
    fi

    local document_filter document_payload document_response document_status document_body
    document_filter="$(build_document_filter)"
    document_payload="$(jq -nc \
        --argjson vector "$VECTOR" \
        --argjson filter "$document_filter" \
        --argjson limit "$DOCUMENT_LIMIT" \
        --argjson score_threshold "$SCORE_THRESHOLD" \
        '{
            query: $vector,
            limit: $limit,
            offset: 0,
            with_payload: true,
            score_threshold: $score_threshold,
            filter: $filter
        }')"

    echo "== document lookup summary ==" >&2
    printf 'document_limit=%s\n' "$DOCUMENT_LIMIT" >&2
    printf 'indexed_only=%s\n' "$INDEXED_ONLY" >&2
    if [[ -n "$BOOK_ID" ]]; then
        printf 'book_id=%s\n' "$BOOK_ID" >&2
    fi
    if [[ -n "$QUESTION" ]]; then
        printf 'question=%s\n' "$QUESTION" >&2
    fi

    if [[ "$SHOW_RAW_PAYLOAD" == "true" ]]; then
        echo >&2
        echo "== document lookup payload ==" >&2
        jq . <<< "$document_payload" >&2
    fi

    echo >&2
    echo "== document lookup response ==" >&2
    document_response="$(run_qdrant "POST" "/collections/${COLLECTION}/points/query" "$document_payload")"
    printf '%s\n' "$document_response" >&2

    document_status="$(printf '%s\n' "$document_response" | extract_status)"
    document_body="$(printf '%s\n' "$document_response" | extract_body)"

    if [[ "$document_status" != "200" ]]; then
        echo >&2
        echo "Outcome: document lookup failed with HTTP ${document_status:-unknown}." >&2
        exit 1
    fi

    jq -ce '[.result.points[].payload.job_id | select(type == "string" and length > 0)] | unique' <<< "$document_body"
}

VECTOR="$(resolve_vector)"
VECTOR_SOURCE="tei_question"
if [[ -n "$QUESTION_VECTOR_JSON" ]]; then
    VECTOR_SOURCE="question_vector_json"
fi

echo "== batch summary =="
printf 'vector_source=%s\n' "$VECTOR_SOURCE"
printf 'vector_dimensions=%s\n' "$(jq 'length' <<< "$VECTOR")"
printf 'query_limit=%s\n' "$QUERY_LIMIT"
printf 'score_threshold=%s\n' "$SCORE_THRESHOLD"
if [[ -n "$QUESTION" ]]; then
    printf 'question=%s\n' "$QUESTION"
fi
if [[ -n "$JOB_ID" ]]; then
    printf 'job_id=%s\n' "$JOB_ID"
fi
if [[ -n "$JOB_IDS" ]]; then
    printf 'job_ids_override=%s\n' "$JOB_IDS"
fi

JOB_ID_ARRAY="$(resolve_job_ids)"

if [[ "$(jq 'length' <<< "$JOB_ID_ARRAY")" -eq 0 ]]; then
    echo
    echo "Outcome: batch skipped; no document matches for this query/filter."
    exit 0
fi

BATCH_PAYLOAD="$(jq -nc \
    --argjson vector "$VECTOR" \
    --argjson score_threshold "$SCORE_THRESHOLD" \
    --argjson limit "$QUERY_LIMIT" \
    --argjson indexed_only "$INDEXED_ONLY" \
    --argjson job_ids "$JOB_ID_ARRAY" '
    {
        searches: [
            $job_ids[] | {
                query: $vector,
                limit: $limit,
                with_payload: true,
                score_threshold: $score_threshold,
                filter: {
                    must: (
                        (if $indexed_only then [{ "key":"indexed", "match":{"value":"true"} }] else [] end) +
                        [
                            { "key":"vector_kind", "match":{"value":"chunk"} },
                            { "key":"job_id", "match":{"value": .} }
                        ]
                    )
                }
            }
        ]
    }')"

echo
echo "== resolved job ids =="
jq . <<< "$JOB_ID_ARRAY"

if [[ "$SHOW_RAW_PAYLOAD" == "true" ]]; then
    echo
    echo "== batch payload =="
    jq . <<< "$BATCH_PAYLOAD"
fi

echo
echo "== batch response =="
BATCH_RESPONSE="$(run_qdrant "POST" "/collections/${COLLECTION}/points/query/batch" "$BATCH_PAYLOAD")"
printf '%s\n' "$BATCH_RESPONSE"

BATCH_STATUS="$(printf '%s\n' "$BATCH_RESPONSE" | extract_status)"
BATCH_BODY="$(printf '%s\n' "$BATCH_RESPONSE" | extract_body)"

if [[ "$BATCH_STATUS" != "200" ]]; then
    echo
    echo "Outcome: batch endpoint failed with HTTP ${BATCH_STATUS:-unknown}."
    exit 1
fi

BATCH_POINT_COUNT="$(jq '[.result[].points | length] | add // 0' <<< "$BATCH_BODY")"
echo
if [[ "$BATCH_POINT_COUNT" == "0" ]]; then
    echo "Outcome: batch endpoint succeeded; no chunk matches for the selected jobs."
else
    echo "Outcome: batch endpoint succeeded; matched chunk points=${BATCH_POINT_COUNT}."
fi

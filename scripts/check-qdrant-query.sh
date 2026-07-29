#!/usr/bin/env bash
set -euo pipefail

COLLECTION="${COLLECTION:-evidence_v2}"
QDRANT_CONTAINER="${QDRANT_CONTAINER:-raglibrarian-qdrant-1}"
QDRANT_URL="${QDRANT_URL:-http://127.0.0.1:6333}"
CURL_IMAGE="${CURL_IMAGE:-curlimages/curl:8.11.0}"
QUERY_LIMIT="${QUERY_LIMIT:-3}"
QUERY_OFFSET="${QUERY_OFFSET:-0}"
SCORE_THRESHOLD="${SCORE_THRESHOLD:-0}"
VECTOR_KIND="${VECTOR_KIND:-chunk}"
INCLUDE_PAYLOAD="${INCLUDE_PAYLOAD:-true}"
INDEXED_ONLY="${INDEXED_ONLY:-true}"
QUESTION_VECTOR_DIMENSIONS="${QUESTION_VECTOR_DIMENSIONS:-768}"
SHOW_RAW_PAYLOAD="${SHOW_RAW_PAYLOAD:-false}"

# Scope filters (optional):
# BOOK_ID  -> filter by book_id
# JOB_ID   -> filter by job_id
#
# Query vector selector:
# QUESTION              -> embed through TEI (normal mode)
# QUESTION_VECTOR_JSON  -> precomputed vector override
#
# Example:
#   QUESTION='vim registers' VECTOR_KIND=document ./scripts/check-qdrant-query.sh
#
BOOK_ID="${BOOK_ID:-}"
JOB_ID="${JOB_ID:-}"
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

build_filter() {
    local filter='{"must":[]}'

    if [[ "$INDEXED_ONLY" == "true" ]]; then
        filter="$(jq -c '.must += [{ "key":"indexed", "match":{"value":"true"} }]' <<< "$filter")"
    fi

    if [[ -n "$VECTOR_KIND" ]]; then
        filter="$(jq -c --arg kind "$VECTOR_KIND" '.must += [{ "key":"vector_kind", "match":{"value":$kind} }]' <<< "$filter")"
    fi

    if [[ -n "$BOOK_ID" ]]; then
        filter="$(jq -c --arg book "$BOOK_ID" '.must += [{ "key":"book_id", "match":{"value":$book} }]' <<< "$filter")"
    fi

    if [[ -n "$JOB_ID" ]]; then
        filter="$(jq -c --arg job "$JOB_ID" '.must += [{ "key":"job_id", "match":{"value":$job} }]' <<< "$filter")"
    fi

    printf '%s' "$filter"
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

FILTER="$(build_filter)"
VECTOR="$(resolve_vector)"
VECTOR_SOURCE="tei_question"
if [[ -n "$QUESTION_VECTOR_JSON" ]]; then
    VECTOR_SOURCE="question_vector_json"
fi

PAYLOAD="$(jq -nc \
    --argjson vector "$VECTOR" \
    --argjson filter "$FILTER" \
    --argjson limit "$QUERY_LIMIT" \
    --argjson offset "$QUERY_OFFSET" \
    --argjson with_payload "$INCLUDE_PAYLOAD" \
    --argjson score_threshold "$SCORE_THRESHOLD" \
    '{
        query: $vector,
        limit: $limit,
        offset: $offset,
        with_payload: $with_payload,
        score_threshold: $score_threshold,
        filter: $filter
    }')"

echo "== query summary =="
printf 'vector_source=%s\n' "$VECTOR_SOURCE"
printf 'vector_dimensions=%s\n' "$(jq 'length' <<< "$VECTOR")"
printf 'vector_kind=%s\n' "$VECTOR_KIND"
printf 'indexed_only=%s\n' "$INDEXED_ONLY"
printf 'query_limit=%s\n' "$QUERY_LIMIT"
printf 'query_offset=%s\n' "$QUERY_OFFSET"
printf 'score_threshold=%s\n' "$SCORE_THRESHOLD"
if [[ -n "$QUESTION" ]]; then
    printf 'question=%s\n' "$QUESTION"
fi
if [[ -n "$BOOK_ID" ]]; then
    printf 'book_id=%s\n' "$BOOK_ID"
fi
if [[ -n "$JOB_ID" ]]; then
    printf 'job_id=%s\n' "$JOB_ID"
fi

if [[ "$SHOW_RAW_PAYLOAD" == "true" ]]; then
    echo
    echo "== request payload =="
    jq . <<< "$PAYLOAD"
fi

echo
echo "== response =="
RESPONSE="$(run_qdrant "POST" "/collections/${COLLECTION}/points/query" "$PAYLOAD")"
printf '%s\n' "$RESPONSE"

STATUS="$(printf '%s\n' "$RESPONSE" | extract_status)"
BODY="$(printf '%s\n' "$RESPONSE" | extract_body)"

if [[ "$STATUS" == "200" ]]; then
    POINT_COUNT="$(jq -r '.result.points | length' <<< "$BODY")"
    echo
    if [[ "$POINT_COUNT" == "0" ]]; then
        echo "Outcome: Qdrant query endpoint succeeded; no semantic matches for this query/filter."
    else
        echo "Outcome: Qdrant query endpoint succeeded; matched points=${POINT_COUNT}."
    fi
else
    echo
    echo "Outcome: Qdrant query endpoint failed with HTTP ${STATUS:-unknown}."
    exit 1
fi

#!/usr/bin/env bash
set -euo pipefail

COLLECTION="${COLLECTION:-evidence_v2}"
QDRANT_CONTAINER="${QDRANT_CONTAINER:-raglibrarian-qdrant-1}"
QDRANT_URL="${QDRANT_URL:-http://127.0.0.1:6333}"
CURL_IMAGE="${CURL_IMAGE:-curlimages/curl:8.11.0}"
SAMPLE_LIMIT="${SAMPLE_LIMIT:-25}"

# Scope filters (optional):
# BOOK_ID  -> filter by book_id
# JOB_ID   -> filter by job_id
#
# Example:
#   BOOK_ID=book-1 ./scripts/check-qdrant-evidence.sh
#
BOOK_ID="${BOOK_ID:-}"
JOB_ID="${JOB_ID:-}"

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

API_KEY="$(get_api_key)"
if [[ -z "${API_KEY}" ]]; then
    echo "No Qdrant read API key found. Set QDRANT_API_KEY or ensure a readable secret exists." >&2
    exit 1
fi

run_qdrant() {
    local method="$1"
    local path="$2"
    local payload="${3:-}"

    if [[ -n "$payload" ]]; then
        docker run --rm --network "container:${QDRANT_CONTAINER}" \
            "$CURL_IMAGE" -sS \
            -H "api-key: ${API_KEY}" \
            -H "Content-Type: application/json" \
            -X "$method" \
            -d "$payload" \
            "${QDRANT_URL}${path}"
    else
        docker run --rm --network "container:${QDRANT_CONTAINER}" \
            "$CURL_IMAGE" -sS \
            -H "api-key: ${API_KEY}" \
            "${QDRANT_URL}${path}"
    fi
}

build_filter() {
    local filter='{"must":[]}'
    if [[ -n "${BOOK_ID}" ]]; then
        filter="$(jq -c --arg book "$BOOK_ID" '.must += [{ "key":"book_id", "match":{"value":$book} }]' <<< "$filter")"
    fi
    if [[ -n "${JOB_ID}" ]]; then
        filter="$(jq -c --arg job "$JOB_ID" '.must += [{ "key":"job_id", "match":{"value":$job} }]' <<< "$filter")"
    fi
    printf '%s' "$filter"
}

base_filter="$(build_filter)"

count_filter() {
    local vector_kind="$1"
    local indexed="$2"
    local filter="$base_filter"

    if [[ -n "$vector_kind" ]]; then
        filter="$(jq -c --arg vk "$vector_kind" '.must += [{ "key":"vector_kind", "match":{"value":$vk} }]' <<< "$filter")"
    fi
    if [[ -n "$indexed" ]]; then
        filter="$(jq -c --arg indexed "$indexed" '.must += [{ "key":"indexed", "match":{"value":$indexed} }]' <<< "$filter")"
    fi

    local payload
    payload="$(jq -c --argjson filter "$filter" '{filter: $filter}' <<< '""')"
    run_qdrant "POST" "/collections/${COLLECTION}/points/count?exact=true" "$payload" | jq -r '.result.count'
}

scroll_points() {
    local vector_kind="$1"
    local indexed="$2"
    local show_payload="$3"
    local with_vector="$4"
    local filter="$base_filter"
    local offset='null'
    local request_count=0

    if [[ -n "$vector_kind" ]]; then
        filter="$(jq -c --arg vk "$vector_kind" '.must += [{ "key":"vector_kind", "match":{"value":$vk} }]' <<< "$filter")"
    fi
    if [[ -n "$indexed" ]]; then
        filter="$(jq -c --arg indexed "$indexed" '.must += [{ "key":"indexed", "match":{"value":$indexed} }]' <<< "$filter")"
    fi

    while true; do
        local payload
        payload="$(jq -c --argjson filter "$filter" --argjson limit "$SAMPLE_LIMIT" --argjson with_payload "$show_payload" --argjson with_vector "$with_vector" --argjson off "$offset" '{
            filter: $filter,
            limit: $limit,
            with_payload: $with_payload,
            with_vector: $with_vector
        } + (if ($off | type) == "null" then {} else {offset: $off} end)' <<< '""')"

        request_count=$((request_count + 1))
        response="$(run_qdrant "POST" "/collections/${COLLECTION}/points/scroll" "$payload")"

        if [[ "$show_payload" == "true" ]]; then
            jq '.result.points[]' <<< "$response"
        else
            jq '.result' <<< "$response"
        fi

        next_offset="$(jq -r '.result.next_page_offset // empty' <<< "$response")"
        if [[ -z "${next_offset}" || "${next_offset}" == "null" ]]; then
            break
        fi
        offset="$(jq '.result.next_page_offset' <<< "$response")"

        if (( request_count >= 10 )); then
            echo "Pagination guard triggered after 10 requests for safety." >&2
            break
        fi
    done
}

echo "== Qdrant collection =="
run_qdrant GET "/collections/${COLLECTION}" | jq '.result | {
    status: (.status // .), points_count, indexed_vectors_count, segments_count, config: .config, metadata: .config.metadata, payload_schema: .payload_schema
}'

echo
echo "== counts =="
echo "chunks:      $(count_filter "chunk" "")"
echo "documents:   $(count_filter "document" "")"
echo "indexed:     $(count_filter "" "true")"
echo "staged:      $(count_filter "" "false")"

echo
echo "== sample indexed chunks =="
scroll_points "chunk" "true" "true" "false" \
    | sed 's/^/  /'

echo
echo "== sample documents (all generations, scoped by filters) =="
scroll_points "document" "" "true" "false" \
    | sed 's/^/  /'

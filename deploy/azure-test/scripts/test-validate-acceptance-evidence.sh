#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)
cd "$repo_root"

expected_commit=0123456789abcdef0123456789abcdef01234567
stale_commit=1111111111111111111111111111111111111111
evidence_file=$(mktemp /tmp/raglibrarian-acceptance-test.XXXXXX.json)
trap 'rm -f "$evidence_file"' EXIT

write_evidence() {
  local mode=$1
  local commit=$2

  jq -n \
    --arg mode "$mode" \
    --arg commit "$commit" \
    '{
      mode: $mode,
      commit: $commit,
      milestones: ["m6", "m4", "m7"],
      sanitized: true,
      secret_leakage: false,
      forbidden_payload_leakage: false,
      m4: {status: "passed"},
      m6: {status: "passed"},
      m7: {status: "passed"}
    }' > "$evidence_file"
}

expect_success() {
  local description=$1
  shift

  if ! "$@" >/dev/null; then
    echo "expected success: $description" >&2
    exit 1
  fi
}

expect_failure() {
  local description=$1
  shift

  if "$@" >/dev/null 2>&1; then
    echo "expected failure: $description" >&2
    exit 1
  fi
}

validator=deploy/azure-test/scripts/validate-acceptance-evidence.sh

write_evidence worker "$expected_commit"
expect_success "worker evidence for expected commit" "$validator" "$evidence_file" worker "$expected_commit"

write_evidence serverless "$expected_commit"
expect_success "serverless evidence for expected commit" "$validator" "$evidence_file" serverless "$expected_commit"

write_evidence worker "$stale_commit"
expect_failure "stale evidence commit" "$validator" "$evidence_file" worker "$expected_commit"

write_evidence worker "$expected_commit"
expect_failure "malformed expected commit" "$validator" "$evidence_file" worker bad
expect_failure "uppercase expected commit" "$validator" "$evidence_file" worker ABCDEF0123456789ABCDEF0123456789ABCDEF01

jq '.diagnostic = "Bearer abcdefghijklmnopqrstuvwxyz"' "$evidence_file" > "${evidence_file}.secret"
mv "${evidence_file}.secret" "$evidence_file"
expect_failure "secret-like evidence text" "$validator" "$evidence_file" worker "$expected_commit"

write_evidence worker "$expected_commit"
jq '.m6.status = "failed"' "$evidence_file" > "${evidence_file}.failed"
mv "${evidence_file}.failed" "$evidence_file"
expect_failure "failed milestone status" "$validator" "$evidence_file" worker "$expected_commit"

write_evidence worker "$expected_commit"
jq '.sanitized = false' "$evidence_file" > "${evidence_file}.unsanitized"
mv "${evidence_file}.unsanitized" "$evidence_file"
expect_failure "unsanitized evidence" "$validator" "$evidence_file" worker "$expected_commit"

echo "acceptance evidence validator regression checks passed"

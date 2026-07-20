#!/usr/bin/env bash
set -euo pipefail

template="${1:-infra/aws/m4/template.yaml}"
[[ -f "$template" ]] || {
  echo "M4 template not found: $template" >&2
  exit 1
}

processing_role="$(awk '
  /^  ProcessingRole:/ { capture = 1 }
  capture && /^  ProcessingFunction:/ { exit }
  capture { print }
' "$template")"

[[ -n "$processing_role" ]] || {
  echo 'M4 ProcessingRole was not found' >&2
  exit 1
}

statement_for_sid() {
  local sid="$1"

  printf '%s\n' "$processing_role" | awk -v sid="$sid" '
    $0 == "              - Sid: " sid { found = 1 }
    found && $0 ~ /^              - Sid: / && $0 != "              - Sid: " sid { exit }
    found { print }
    END { if (!found) exit 1 }
  '
}

require_line() {
  local statement="$1"
  local expected="$2"

  printf '%s\n' "$statement" | grep -Fqx -- "$expected" || {
    echo "M4 ProcessingRole is missing required policy line: $expected" >&2
    exit 1
  }
}

require_count() {
  local statement="$1"
  local pattern="$2"
  local expected="$3"
  local actual

  actual="$(printf '%s\n' "$statement" | grep -Ec -- "$pattern" || true)"
  [[ "$actual" == "$expected" ]] || {
    echo "M4 ProcessingRole policy shape is broader than expected" >&2
    exit 1
  }
}

reject_text() {
  local statement="$1"
  local forbidden="$2"

  if printf '%s\n' "$statement" | grep -Fq -- "$forbidden"; then
    echo "M4 ProcessingRole contains forbidden policy text: $forbidden" >&2
    exit 1
  fi
}

abort_statement="$(statement_for_sid AbortPartialArtifacts)" || {
  echo 'M4 ProcessingRole is missing AbortPartialArtifacts' >&2
  exit 1
}
require_line "$abort_statement" '                  - s3:DeleteObject'
require_line "$abort_statement" '                  - s3:DeleteObjectVersion'
require_line "$abort_statement" '                Effect: Allow'
require_line "$abort_statement" "                Resource: !Sub '\${ArtifactBucket.Arn}/books/*'"
require_count "$abort_statement" '^                  - s3:' 2
require_count "$abort_statement" '^                Resource:' 1
reject_text "$abort_statement" 's3:*'
reject_text "$abort_statement" 'SourceBucketName'

list_statement="$(statement_for_sid ListPartialArtifactVersions)" || {
  echo 'M4 ProcessingRole is missing ListPartialArtifactVersions' >&2
  exit 1
}
require_line "$list_statement" '                Action: s3:ListBucketVersions'
require_line "$list_statement" '                Effect: Allow'
require_line "$list_statement" '                Resource: !GetAtt ArtifactBucket.Arn'
require_line "$list_statement" '                Condition:'
require_line "$list_statement" '                  StringLike:'
require_line "$list_statement" "                    s3:prefix: 'books/*'"
require_count "$list_statement" '^                Action:' 1
require_count "$list_statement" '^                Resource:' 1
require_count "$list_statement" '^                    s3:prefix:' 1
reject_text "$list_statement" 's3:*'
reject_text "$list_statement" 'SourceBucketName'

echo 'M4 ProcessingRole artifact-abort policy is scoped correctly'

#!/usr/bin/env bash
set -euo pipefail

evidence_file=${1:?usage: validate-acceptance-evidence.sh EVIDENCE_FILE MODE EXPECTED_COMMIT}
mode=${2:?usage: validate-acceptance-evidence.sh EVIDENCE_FILE MODE EXPECTED_COMMIT}
expected_commit=${3:?usage: validate-acceptance-evidence.sh EVIDENCE_FILE MODE EXPECTED_COMMIT}

case "$mode" in
  worker|serverless) ;;
  *) echo "mode must be worker or serverless" >&2; exit 2 ;;
esac

if [[ ! "$expected_commit" =~ ^[0-9a-f]{40}$ ]]; then
  echo "expected commit must be a lowercase 40-character Git SHA" >&2
  exit 2
fi

test -f "$evidence_file"

jq -e --arg mode "$mode" --arg expected_commit "$expected_commit" '
  .mode == $mode and
  (.milestones | sort == ["m4","m6","m7"]) and
  (.commit == $expected_commit) and
  (.m4.status == "passed") and
  (.m6.status == "passed") and
  (.m7.status == "passed") and
  (.sanitized == true) and
  ((.forbidden_payload_leakage // false) == false) and
  ((.secret_leakage // false) == false)
' "$evidence_file" >/dev/null

if jq -e 'any(.. | strings; test("(BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY|Bearer [A-Za-z0-9._-]{20,}|sk-[A-Za-z0-9_-]{20,}|password=|prompt=|connection string)"; "i"))' "$evidence_file" >/dev/null; then
  echo "acceptance evidence contains forbidden secret-like text" >&2
  exit 1
fi

jq -r '"mode=\(.mode) commit=\(.commit) m4=\(.m4.status) m6=\(.m6.status) m7=\(.m7.status)"' "$evidence_file"

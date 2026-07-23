#!/usr/bin/env bash
set -euo pipefail

evidence_file=${1:?usage: validate-acceptance-evidence.sh EVIDENCE_FILE MODE}
mode=${2:?usage: validate-acceptance-evidence.sh EVIDENCE_FILE MODE}

case "$mode" in
  worker|serverless) ;;
  *) echo "mode must be worker or serverless" >&2; exit 2 ;;
esac

test -f "$evidence_file"

jq -e --arg mode "$mode" '
  .mode == $mode and
  (.milestones | sort == ["m4","m6","m7"]) and
  (.commit | type == "string" and test("^[0-9a-f]{40}$")) and
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

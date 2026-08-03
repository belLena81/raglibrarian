#!/usr/bin/env bash
set -euo pipefail

command -v screen >/dev/null 2>&1 || exit 0

sessions=(
  raglibrarian-identity
  raglibrarian-catalog
  raglibrarian-ingestion
  raglibrarian-layout-worker
  raglibrarian-retrieval
  raglibrarian-retrieval-worker
  raglibrarian-answer
  raglibrarian-edge
)

for session in "${sessions[@]}"; do
  screen -wipe >/dev/null 2>&1 || true
  while IFS= read -r session_id; do
    [[ -n "$session_id" ]] || continue
    screen -S "$session_id" -X quit || true
    echo "Stopped screen session: $session_id"
  done < <(
    screen -ls 2>/dev/null | awk -v session="$session" '
      $1 ~ ("[.]" session "$") && ($NF == "(Detached)" || $NF == "(Attached)") { print $1 }
    '
  )
done

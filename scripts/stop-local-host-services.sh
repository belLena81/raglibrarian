#!/usr/bin/env bash
set -euo pipefail

command -v screen >/dev/null 2>&1 || exit 0

sessions=(
  raglibrarian-identity
  raglibrarian-catalog
  raglibrarian-ingestion
  raglibrarian-retrieval
  raglibrarian-retrieval-worker
  raglibrarian-answer
  raglibrarian-edge
)

for session in "${sessions[@]}"; do
  screen -wipe >/dev/null 2>&1 || true
  if screen -ls 2>/dev/null | grep -Eq "\\.${session}[[:space:]]+\\((Detached|Attached)\\)"; then
    screen -S "$session" -X quit || true
    echo "Stopped screen session: $session"
  fi
done

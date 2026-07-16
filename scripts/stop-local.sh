#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

ui_pid_file=.dev/ui.pid
if [[ -r "$ui_pid_file" ]]; then
	ui_pid="$(cat "$ui_pid_file")"
	if [[ "$ui_pid" =~ ^[0-9]+$ ]] && kill -0 "$ui_pid" 2>/dev/null; then
		kill "$ui_pid"
		echo "Stopped UI (PID $ui_pid)."
	fi
	rm -f "$ui_pid_file"
fi

docker compose down
echo "Local stack stopped. Persistent PostgreSQL, MinIO, and RabbitMQ volumes were retained."
echo "To intentionally reset all local data: docker compose down -v"

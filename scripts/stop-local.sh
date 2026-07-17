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

log_pid_dir=.dev/log-pids
if [[ -d "$log_pid_dir" ]]; then
	for log_pid_file in "$log_pid_dir"/*.pid; do
		[[ -r "$log_pid_file" ]] || continue
		log_pid="$(cat "$log_pid_file")"
		if [[ "$log_pid" =~ ^[0-9]+$ ]] && kill -0 "$log_pid" 2>/dev/null; then
			kill "$log_pid"
		fi
		rm -f "$log_pid_file"
	done
	rmdir "$log_pid_dir" 2>/dev/null || true
fi

docker compose down
echo "Local stack stopped. Persistent PostgreSQL, MinIO, and RabbitMQ volumes were retained."
echo "To intentionally reset all local data: docker compose down -v"

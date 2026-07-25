#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_root="$(mktemp -d /tmp/raglibrarian-run-local-wait.XXXXXX)"
trap 'rm -rf "$test_root"' EXIT

mkdir -p "$test_root/bin" "$test_root/model"

cat > "$test_root/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

log_file="${DOCKER_LOG_FILE:?}"
printf '%s\n' "$*" >> "$log_file"

case "$1" in
  compose)
    shift
    case " $* " in
      *"--profile raglibrarian ps --all -q db-bootstrap"*)
        printf '%s\n' completed-bootstrap
        ;;
      *"--profile raglibrarian ps --all -q minio-bootstrap"*)
        printf '%s\n' failed-bootstrap
        ;;
      *"--profile raglibrarian logs --no-color --tail 120 minio-bootstrap"*)
        printf '%s\n' "bootstrap logs"
        ;;
      *)
        printf '%s\n' "unexpected compose invocation" >&2
        exit 1
        ;;
    esac
    ;;
  inspect)
    case "$3:$4" in
      "{{.State.Status}}:completed-bootstrap")
        printf '%s\n' exited
        ;;
      "{{.State.ExitCode}}:completed-bootstrap")
        printf '%s\n' 0
        ;;
      "{{.State.Status}}:failed-bootstrap")
        printf '%s\n' exited
        ;;
      "{{.State.ExitCode}}:failed-bootstrap")
        printf '%s\n' 3
        ;;
      *)
        printf '%s\n' "unexpected inspect invocation" >&2
        exit 1
        ;;
    esac
    ;;
  *)
    printf '%s\n' "unexpected docker invocation" >&2
    exit 1
    ;;
esac
EOF
chmod 0755 "$test_root/bin/docker"

docker_log_file="$test_root/docker-argv.log"
touch "$docker_log_file"

export PATH="$test_root/bin:/usr/bin:/bin"
export DOCKER_LOG_FILE="$docker_log_file"
export M5_MODEL_DIR="$test_root/model"
compose_wait_timeout=1
source "$root_dir/scripts/run-local-lib.sh"

wait_for_service_completed_successfully db-bootstrap 1 db-bootstrap

if wait_for_service_completed_successfully minio-bootstrap 1 minio-bootstrap; then
  echo "expected failed bootstrap to be reported as failure" >&2
  exit 1
fi

grep -F -- '--profile raglibrarian ps --all -q db-bootstrap' "$docker_log_file" >/dev/null || {
  echo "wait helper did not query exited bootstrap containers with --all" >&2
  exit 1
}

grep -F -- '--profile raglibrarian logs --no-color --tail 120 minio-bootstrap' "$docker_log_file" >/dev/null || {
  echo "failed bootstrap did not emit logs" >&2
  exit 1
}

echo "run-local wait helper regression passed"

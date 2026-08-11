#!/usr/bin/env bash
set -eu

script_dir="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

cat >"$tmp_dir/docker" <<'EOF'
#!/usr/bin/env bash
set -eu
count=0
if [ -f "$BUILD_ATTEMPT_FILE" ]; then
  count="$(cat "$BUILD_ATTEMPT_FILE")"
fi
count=$((count + 1))
printf '%s\n' "$count" >"$BUILD_ATTEMPT_FILE"
printf '%s\n' "$*" >"$BUILD_ARGS_FILE"
if [ "$count" -le "${BUILD_FAILURES:-0}" ]; then
  exit 1
fi
EOF

cat >"$tmp_dir/sleep" <<'EOF'
#!/usr/bin/env bash
set -eu
printf '%s\n' "$1" >>"$BUILD_SLEEP_FILE"
EOF
chmod +x "$tmp_dir/docker" "$tmp_dir/sleep"

run_build() {
  PATH="$tmp_dir:$PATH" \
    BUILD_ATTEMPT_FILE="$tmp_dir/attempts" \
    BUILD_ARGS_FILE="$tmp_dir/args" \
    BUILD_SLEEP_FILE="$tmp_dir/sleeps" \
    "$script_dir/build-image-ci.sh" test-image:test test-scope --target service-runtime
}

reset_state() {
  rm -f "$tmp_dir/attempts" "$tmp_dir/args" "$tmp_dir/sleeps"
}

reset_state
BUILD_FAILURES=0 run_build
test "$(cat "$tmp_dir/attempts")" = 1
grep -q -- 'buildx build --load --cache-from type=gha,scope=test-scope' "$tmp_dir/args"
grep -q -- '--target service-runtime -t test-image:test .' "$tmp_dir/args"

reset_state
BUILD_FAILURES=2 IMAGE_BUILD_ATTEMPTS=3 IMAGE_BUILD_RETRY_BASE_SECONDS=5 run_build
test "$(cat "$tmp_dir/attempts")" = 3
test "$(sed -n '1p' "$tmp_dir/sleeps")" = 5
test "$(sed -n '2p' "$tmp_dir/sleeps")" = 10

reset_state
BUILD_FAILURES=1 IMAGE_BUILD_ATTEMPTS=02 IMAGE_BUILD_RETRY_BASE_SECONDS=08 run_build
test "$(cat "$tmp_dir/attempts")" = 2
test "$(cat "$tmp_dir/sleeps")" = 8

reset_state
if BUILD_FAILURES=3 IMAGE_BUILD_ATTEMPTS=3 IMAGE_BUILD_RETRY_BASE_SECONDS=1 run_build; then
  echo "Expected exhausted image build retries to fail" >&2
  exit 1
fi
test "$(cat "$tmp_dir/attempts")" = 3

for invalid in 0 -1 abc '1.5'; do
  reset_state
  if IMAGE_BUILD_ATTEMPTS="$invalid" run_build >/dev/null 2>&1; then
    echo "Expected IMAGE_BUILD_ATTEMPTS=$invalid to fail" >&2
    exit 1
  fi
  test ! -f "$tmp_dir/attempts"
done

for invalid in 0 -1 abc '1.5'; do
  reset_state
  if IMAGE_BUILD_RETRY_BASE_SECONDS="$invalid" run_build >/dev/null 2>&1; then
    echo "Expected IMAGE_BUILD_RETRY_BASE_SECONDS=$invalid to fail" >&2
    exit 1
  fi
  test ! -f "$tmp_dir/attempts"
done

echo "Image build retry tests passed"

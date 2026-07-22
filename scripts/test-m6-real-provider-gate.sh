#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_root="$(mktemp -d /tmp/raglibrarian-m6-real-gate.XXXXXX)"
trap 'rm -rf "$test_root"' EXIT
mkdir -p "$test_root/bin"

for file in key reader librarian; do
	: > "$test_root/$file"
	chmod 400 "$test_root/$file"
done

cat > "$test_root/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
: "${M6_FAKE_STATE:?}"
: "${M6_FAKE_KEY:?}"

if [[ "$1" == compose ]]; then
	shift
	case " $* " in
		*" ps -q answer-service "*)
			if [[ -f "$M6_FAKE_STATE/recreated" ]]; then echo new-answer-container; else echo old-answer-container; fi
			;;
		*" up -d --no-deps --force-recreate --wait --wait-timeout 120 answer-service "*)
			[[ "${ANSWER_LLM_BASE_URL:-}" == "https://provider.example.test" ]]
			[[ "${ANSWER_LLM_MODEL:-}" == "provider-model" ]]
			[[ "${ANSWER_LLM_API_KEY_PATH:-}" == "$M6_FAKE_KEY" ]]
			: > "$M6_FAKE_STATE/recreated"
			;;
		*) echo "unexpected fake Compose invocation" >&2; exit 1 ;;
	esac
	exit 0
fi

if [[ "$1" == inspect && "$2" == --format && "$4" == new-answer-container ]]; then
	case "$3" in
		*Config.Env*)
			printf 'ANSWER_LLM_BASE_URL=%s\nANSWER_LLM_MODEL=%s\n' "$ANSWER_LLM_BASE_URL" "$ANSWER_LLM_MODEL"
			;;
		*Mounts*) printf '%s\n' "$M6_FAKE_KEY" ;;
		*) echo "unexpected fake inspect format" >&2; exit 1 ;;
	esac
	exit 0
fi

echo "unexpected fake Docker invocation" >&2
exit 1
EOF
chmod 700 "$test_root/bin/docker"

cat > "$test_root/bin/recursive-make" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ " $* " == *" M6_E2E_PATTERN=^TestM6SearchRemainsCompatibleAndAnswerCitesReturnedEvidence$ "* ]]
[[ " $* " == *" m6-e2e "* ]]
: > "${M6_FAKE_STATE:?}/e2e"
EOF
chmod 700 "$test_root/bin/recursive-make"

cd "$root_dir"
PATH="$test_root/bin:$PATH" \
M6_FAKE_STATE="$test_root" \
M6_FAKE_KEY="$test_root/key" \
ANSWER_LLM_BASE_URL=https://provider.example.test \
ANSWER_LLM_MODEL=provider-model \
ANSWER_LLM_API_KEY_PATH="$test_root/key" \
M5_E2E_READER_TOKEN_FILE="$test_root/reader" \
M5_E2E_LIBRARIAN_TOKEN_FILE="$test_root/librarian" \
make --no-print-directory MAKE="$test_root/bin/recursive-make" m6-answer-quality-test-real

[[ -f "$test_root/recreated" && -f "$test_root/e2e" ]] || {
	echo "M6 real-provider gate did not recreate Answer and run focused E2E" >&2
	exit 1
}

echo "M6 real-provider gate regressions passed"

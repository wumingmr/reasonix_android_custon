#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
root="$(mktemp -d "${TMPDIR:-/tmp}/reasonix-stable-dispatch-test.XXXXXX")"
cleanup() {
	case "$root" in */reasonix-stable-dispatch-test.*) rm -rf -- "$root" ;; *) exit 1 ;; esac
}
trap cleanup EXIT

mkdir -p "$root/bin"
cat >"$root/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
body="$(cat)"
test "$1" = api
test "$2" = -X
test "$3" = POST
test "$4" = "repos/esengine/DeepSeek-Reasonix/actions/workflows/release-promote.yml/dispatches"
jq -e '.ref == "main-v2" and .inputs.candidate_id == "v1.2.3-aaaaaaaaaaaa-bbbbbbbbbbbb" and (.inputs.operation == "publish" or .inputs.operation == "recover")' <<<"$body" >/dev/null
EOF
chmod +x "$root/bin/gh"

candidate=v1.2.3-aaaaaaaaaaaa-bbbbbbbbbbbb
PATH="$root/bin:$PATH" "$repo_root/scripts/release-stable.sh" "$candidate"
PATH="$root/bin:$PATH" "$repo_root/scripts/release-stable.sh" "$candidate" recover

for invalid in 1.2.3 v1.2.3-short v1.2.3-aaaaaaaaaaaa-bbbbbbbbbbbz; do
	if PATH="$root/bin:$PATH" "$repo_root/scripts/release-stable.sh" "$invalid" >/dev/null 2>&1; then
		echo "invalid candidate unexpectedly dispatched: $invalid" >&2
		exit 1
	fi
done
if PATH="$root/bin:$PATH" "$repo_root/scripts/release-stable.sh" "$candidate" retry >/dev/null 2>&1; then
	echo "invalid operation unexpectedly dispatched" >&2
	exit 1
fi

echo "stable candidate dispatch tests: PASS"

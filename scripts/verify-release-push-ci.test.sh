#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/reasonix-push-ci-test.XXXXXX")"
trap 'rm -rf -- "$test_root"' EXIT

git -C "$test_root" init -q
git -C "$test_root" config user.name test
git -C "$test_root" config user.email test@example.invalid
printf 'code\n' >"$test_root/product.txt"
git -C "$test_root" add product.txt
git -C "$test_root" commit -qm code
code_sha="$(git -C "$test_root" rev-parse HEAD)"
mkdir -p "$test_root/release-notes"
printf '{}\n' >"$test_root/release-notes/releases.json"
git -C "$test_root" add release-notes/releases.json
git -C "$test_root" commit -qm notes
notes_sha="$(git -C "$test_root" rev-parse HEAD)"

mkdir -p "$test_root/bin"
cat >"$test_root/bin/gh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
sha=""
while [ "$#" -gt 0 ]; do
	if [ "$1" = --commit ]; then sha="$2"; shift 2; else shift; fi
done
[ -n "$sha" ]
printf '%s\n' "$sha" >>"$GH_QUERY_LOG"
printf '[{"headSha":"%s","status":"completed","conclusion":"success"}]\n' "$sha"
SH
chmod +x "$test_root/bin/gh"

run_verify() {
	local sha="$1"
	: >"$test_root/queries"
	(
		cd "$test_root"
		PATH="$test_root/bin:$PATH" GH_QUERY_LOG="$test_root/queries" \
			RELEASE_CI_WAIT_SECONDS=0 RELEASE_CI_POLL_SECONDS=1 \
			bash "$repo_root/scripts/verify-release-push-ci.sh" "$sha"
	)
}

run_verify "$notes_sha"
[ "$(cat "$test_root/queries")" = "$code_sha" ]

run_verify "$code_sha"
[ "$(cat "$test_root/queries")" = "$code_sha" ]

printf 'changed\n' >>"$test_root/product.txt"
git -C "$test_root" add product.txt
git -C "$test_root" commit -qm mixed
mixed_sha="$(git -C "$test_root" rev-parse HEAD)"
run_verify "$mixed_sha"
[ "$(cat "$test_root/queries")" = "$mixed_sha" ]

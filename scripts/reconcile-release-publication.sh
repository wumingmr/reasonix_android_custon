#!/usr/bin/env bash
# One post-publication owner for fresh observations and durable evidence.
set -euo pipefail
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repository="${RELEASE_REPOSITORY:?}"
version="${RELEASE_VERSION:?}"
operation="${RELEASE_OPERATION:?}"
expected_sha="${RELEASE_EXPECTED_SHA:?}"
output="${RELEASE_LEDGER_OUTPUT:?}"
[[ "$version" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || exit 2
[[ "$expected_sha" =~ ^[0-9a-f]{40}$ ]] || exit 2
[[ "$operation" =~ ^(publish|recover)$ ]] || exit 2
test "$repository" = esengine/DeepSeek-Reasonix || exit 2
if [ "${GITHUB_ACTIONS:-}" = true ]; then
	test "$GITHUB_REPOSITORY" = "$repository"
	test "$GITHUB_REF" = refs/heads/main-v2
	test "$GITHUB_REF_PROTECTED" = true
fi
work="$(mktemp -d)"
mkdir -p "$(dirname "$output")"
state=verification-failed
stage=immutable
jq -n --arg version "$version" --arg sha "$expected_sha" --arg operation "$operation" \
	'{schema:1,version:$version,sourceSHA:$sha,operation:$operation,surfaces:{}}' > "$work/ledger.json"
finish() {
	local status=$?
	trap - EXIT
	jq --arg state "$state" --arg stage "$stage" \
		--arg run "${GITHUB_RUN_ID:-local}" --arg attempt "${GITHUB_RUN_ATTEMPT:-1}" \
		--arg control "${GITHUB_SHA:-}" --argjson exitCode "$status" \
		'. + {observedAt:(now|todateiso8601),completionState:$state,verificationContext:{stage:$stage,exitCode:$exitCode,runId:$run,runAttempt:$attempt,controlSHA:$control}}' \
		"$work/ledger.json" > "$output" || status=1
	rm -rf -- "$work"
	exit "$status"
}
trap finish EXIT
export CLI_TAG="v$version" DESKTOP_TAG="desktop-v$version"
RELEASE_LEDGER_OUTPUT="$work/verified.json" VERIFY_PUBLIC_POINTERS=true \
	bash "$script_dir/verify-stable-release-artifacts.sh"
cp "$work/verified.json" "$work/ledger.json"
state=release-event-pending
stage=release-event
node "$script_dir/release-event.mjs" generate --version "$version" --sha "$expected_sha" \
	--published-at "$(gh api "repos/$repository/releases/tags/v$version" --jq .published_at)" \
	--output "$work/release-event.json"
has_event="$(gh release view "v$version" --repo "$repository" --json assets --jq '[.assets[] | select(.name == "release-event.json")] | length')"
if [ "$has_event" = 1 ]; then
	gh release download "v$version" --repo "$repository" --pattern release-event.json --output "$work/existing-event.json"
	cmp -s "$work/release-event.json" "$work/existing-event.json" || { echo 'Published release event conflicts with the verified identity' >&2; exit 1; }
elif [ "$has_event" = 0 ]; then
	gh release upload "v$version" --repo "$repository" "$work/release-event.json"
else
	echo 'Release carries more than one release-event.json' >&2
	exit 1
fi
if jq -e '.surfaces.stableManifest' "$work/ledger.json" >/dev/null; then
	state=complete
else
	state=immutable-complete-newer-pointer-preserved
fi
stage=complete

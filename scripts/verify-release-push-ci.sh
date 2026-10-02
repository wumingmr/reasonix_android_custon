#!/usr/bin/env bash
# Wait for successful push CI on one immutable main-v2 candidate.
set -euo pipefail

if [ "$#" -ne 1 ] || [[ ! "$1" =~ ^[0-9a-f]{40}$ ]]; then
	echo "usage: verify-release-push-ci.sh FULL_COMMIT_SHA" >&2
	exit 2
fi

candidate="$1"
repository="${RELEASE_REPOSITORY:-esengine/DeepSeek-Reasonix}"
wait_seconds="${RELEASE_CI_WAIT_SECONDS:-1800}"
poll_seconds="${RELEASE_CI_POLL_SECONDS:-10}"

if [[ ! "$wait_seconds" =~ ^[0-9]+$ ]] || [[ ! "$poll_seconds" =~ ^[1-9][0-9]*$ ]]; then
	echo "RELEASE_CI_WAIT_SECONDS must be non-negative and RELEASE_CI_POLL_SECONDS must be positive" >&2
	exit 2
fi
for command in gh jq git; do
	command -v "$command" >/dev/null || {
		echo "required command is unavailable: $command" >&2
		exit 2
	}
done

wait_for_success() {
	local sha="$1" deadline runs run_status conclusion
	deadline=$((SECONDS + wait_seconds))
	while :; do
		runs="$(gh run list --repo "$repository" --workflow ci.yml --commit "$sha" \
			--event push --limit 20 --json headSha,status,conclusion 2>/dev/null || true)"
		run_status="$(jq -r --arg sha "$sha" \
			'[.[] | select(.headSha == $sha)][0].status // ""' <<<"$runs" 2>/dev/null || true)"
		conclusion="$(jq -r --arg sha "$sha" \
			'[.[] | select(.headSha == $sha and .status == "completed")][0].conclusion // ""' <<<"$runs" 2>/dev/null || true)"
		case "$conclusion" in
		success)
			return 0
			;;
		failure | cancelled | timed_out | action_required | startup_failure)
			echo "CI for $sha concluded $conclusion" >&2
			return 1
			;;
		esac
		if [ "$SECONDS" -ge "$deadline" ]; then
			echo "timed out waiting for successful main-v2 CI on $sha (last status: ${run_status:-missing})" >&2
			return 1
		fi
		sleep "$poll_seconds"
	done
}

# A commit whose only changes are under release-notes/ skips the code matrix
# in ci.yml (the `changes` job's notes_only output). Its code is identical to
# its first parent, so the evidence is the nearest first-parent ancestor that
# changed anything else. A root commit is treated as code.
notes_only_commit() {
	local sha="$1" files
	git rev-parse --verify -q "$sha^" >/dev/null || return 1
	files="$(git diff --name-only "$sha^" "$sha")"
	[ -n "$files" ] || return 1
	! grep -qvE '^release-notes/' <<<"$files"
}

sha="$candidate"
hops=0
while notes_only_commit "$sha"; do
	hops=$((hops + 1))
	if [ "$hops" -gt 5 ]; then
		echo "$candidate sits behind more than 5 release-notes-only commits; refusing to infer its code CI" >&2
		exit 1
	fi
	sha="$(git rev-parse "$sha^")"
	echo "$candidate changes only release-notes/; requiring push CI on ancestor $sha"
	wait_for_success "$sha"
	echo "successful main-v2 push CI verified for code ancestor: $sha"
done
if [ "$sha" = "$candidate" ]; then
	wait_for_success "$candidate"
	echo "successful main-v2 push CI verified: $candidate"
fi

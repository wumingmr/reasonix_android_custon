#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
root="$(mktemp -d "${TMPDIR:-/tmp}/reasonix-candidate-source-test.XXXXXX")"
cleanup() {
	case "$root" in */reasonix-candidate-source-test.*) rm -rf -- "$root" ;; *) exit 1 ;; esac
}
trap cleanup EXIT

remote="$root/remote.git"
work="$root/work"
git init -q --bare "$remote"
git init -q -b main-v2 "$work"
git -C "$work" config user.name test
git -C "$work" config user.email test@example.invalid
mkdir -p "$work/release-notes"
printf '{"releases":[]}\n' >"$work/release-notes/releases.json"
git -C "$work" add .
git -C "$work" commit -q -m base
cat >"$work/release-notes/releases.json" <<'JSON'
{"releases":[{"version":"1.2.3","channel":"stable","status":"reviewed"}]}
JSON
git -C "$work" add .
git -C "$work" commit -q -m notes
candidate="$(git -C "$work" rev-parse HEAD)"
git -C "$work" commit --allow-empty -q -m "unrelated later merge"
git -C "$work" remote add origin "$remote"
git -C "$work" push -q origin main-v2

(
	cd "$work"
	RELEASE_REMOTE=origin "$repo_root/scripts/validate-release-candidate-source.sh" 1.2.3 "$candidate"
)

git -C "$work" tag v1.2.3 "$candidate"
git -C "$work" push -q origin v1.2.3
(
	cd "$work"
	RELEASE_REMOTE=origin "$repo_root/scripts/validate-release-candidate-source.sh" 1.2.3 "$candidate" rehearsal
)
if (
	cd "$work"
	RELEASE_REMOTE=origin "$repo_root/scripts/validate-release-candidate-source.sh" 1.2.3 "$candidate"
); then
	echo "existing release identity unexpectedly passed candidate preparation" >&2
	exit 1
fi
if (
	cd "$work"
	RELEASE_REMOTE=origin "$repo_root/scripts/validate-release-candidate-source.sh" 1.2.3 "$candidate" unsafe
); then
	echo "unknown candidate purpose unexpectedly passed" >&2
	exit 1
fi

echo "release candidate source tests: PASS"

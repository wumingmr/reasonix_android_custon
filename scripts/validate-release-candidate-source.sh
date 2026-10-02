#!/usr/bin/env bash
# Validate an untagged Stable candidate without requiring it to remain the tip
# of main-v2 or to be the commit that introduced its reviewed release notes.
set -euo pipefail

if [ "$#" -lt 2 ] || [ "$#" -gt 3 ] || [[ ! "$1" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || [[ ! "$2" =~ ^[0-9a-f]{40}$ ]] || [[ ! "${3:-release}" =~ ^(release|rehearsal)$ ]]; then
	echo "usage: validate-release-candidate-source.sh MAJOR.MINOR.PATCH FULL_COMMIT_SHA [release|rehearsal]" >&2
	exit 2
fi

version="$1"
candidate="$2"
remote="${RELEASE_REMOTE:-origin}"
purpose="${3:-release}"

git fetch --quiet "$remote" main-v2 --tags
main_sha="$(git rev-parse "$remote/main-v2^{commit}")"
git cat-file -e "$candidate^{commit}" 2>/dev/null || {
	echo "candidate commit is unavailable: $candidate" >&2
	exit 1
}
git merge-base --is-ancestor "$candidate" "$main_sha" || {
	echo "candidate $candidate is not on main-v2 history at $main_sha" >&2
	exit 1
}

for tag in "v$version" "npm-v$version" "desktop-v$version"; do
	if [ "$purpose" = release ] && git show-ref --verify --quiet "refs/tags/$tag"; then
		echo "candidate preparation refuses existing release tag: $tag" >&2
		exit 1
	fi
done

catalog="$(mktemp "${TMPDIR:-/tmp}/reasonix-candidate-catalog.XXXXXX")"
trap 'rm -f -- "$catalog"' EXIT
git show "$candidate:release-notes/releases.json" >"$catalog"
jq -e --arg version "$version" '
  [.releases[] | select(.version == $version and .channel == "stable" and .status == "reviewed")] | length == 1
' "$catalog" >/dev/null || {
	echo "candidate must contain exactly one reviewed Stable release record for $version" >&2
	exit 1
}

echo "candidate source verified: version=$version sha=$candidate main=$main_sha"

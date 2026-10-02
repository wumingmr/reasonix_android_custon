#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 4 ] || [[ ! "$1" =~ ^(check|activate)$ ]] || [[ ! "$2" =~ ^(publish|recover)$ ]] ||
	[[ ! "$3" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || [[ ! "$4" =~ ^[0-9a-f]{40}$ ]]; then
	echo "usage: release-candidate-tags.sh check|activate publish|recover VERSION FULL_SHA" >&2
	exit 2
fi

stage="$1"
operation="$2"
version="$3"
source_sha="$4"
remote="${RELEASE_REMOTE:-origin}"
tags=("v$version" "npm-v$version" "desktop-v$version")
refs=()
for tag in "${tags[@]}"; do refs+=("refs/tags/$tag" "refs/tags/$tag^{}"); done

observe() {
	local listing tag oid peeled
	present=0
	listing="$(git ls-remote --tags "$remote" "${refs[@]}")"
	for tag in "${tags[@]}"; do
		oid="$(awk -v ref="refs/tags/$tag" '$2 == ref { print $1 }' <<<"$listing")"
		peeled="$(awk -v ref="refs/tags/$tag^{}" '$2 == ref { print $1 }' <<<"$listing")"
		if [ -n "$oid" ]; then
			present=$((present + 1))
			test "${peeled:-$oid}" = "$source_sha" || {
				echo "release tag content conflict: $tag" >&2
				return 1
			}
		fi
	done
	if [ "$present" -ne 0 ] && [ "$present" -ne 3 ]; then
		echo "partial release tag identity; refusing to create or move tags" >&2
		return 1
	fi
}

observe
if [ "$operation" = publish ] && [ "$present" -ne 0 ]; then
	echo "release tags already exist; use recover for this candidate" >&2
	exit 1
fi
if [ "$stage" = activate ] && [ "$present" -eq 0 ]; then
	git cat-file -e "$source_sha^{commit}"
	git push --atomic "$remote" \
		"$source_sha:refs/tags/${tags[0]}" \
		"$source_sha:refs/tags/${tags[1]}" \
		"$source_sha:refs/tags/${tags[2]}"
	observe
	test "$present" -eq 3
fi
echo "release tag identity verified: stage=$stage operation=$operation present=$present sha=$source_sha"

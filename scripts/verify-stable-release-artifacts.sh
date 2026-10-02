#!/usr/bin/env bash
# Verify that a stable orchestration produced every public release channel.
set -euo pipefail

repository="${RELEASE_REPOSITORY:?RELEASE_REPOSITORY is required}"
version="${RELEASE_VERSION:?RELEASE_VERSION is required}"
cli_tag="${CLI_TAG:?CLI_TAG is required}"
desktop_tag="${DESKTOP_TAG:?DESKTOP_TAG is required}"
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
attempts="${VERIFY_ATTEMPTS:-6}"
delay="${VERIFY_DELAY_SECONDS:-10}"
verify_pointers="${VERIFY_PUBLIC_POINTERS:-false}"
operation="${RELEASE_OPERATION:-publish}"
ledger_output="${RELEASE_LEDGER_OUTPUT:-}"
# A frozen 1.x line publishes npm under the legacy-v1 dist-tag and leaves the
# latest tag, the Homebrew cask and the CLI pointers to the line that owns them.
frozen=false
npm_tag=latest
if [ "${CLI_PUBLISH_FROZEN:-}" = "true" ]; then
	frozen=true
	npm_tag=legacy-v1
fi

if [[ ! "$version" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
	echo "::error::RELEASE_VERSION must be stable semver, got: $version" >&2
	exit 1
fi
if [ "$cli_tag" != "v$version" ] || [ "$desktop_tag" != "desktop-v$version" ]; then
	echo "::error::release tags do not match version $version: cli=$cli_tag desktop=$desktop_tag" >&2
	exit 1
fi
case "$operation" in publish | recover) ;; *) echo "::error::RELEASE_OPERATION must be publish or recover" >&2; exit 1 ;; esac

release_git_url="https://github.com/${repository}.git"
cli_sha="$(git ls-remote --tags --refs "$release_git_url" "refs/tags/$cli_tag" | awk 'NR == 1 {print $1}')"
npm_sha="$(git ls-remote --tags --refs "$release_git_url" "refs/tags/npm-v$version" | awk 'NR == 1 {print $1}')"
desktop_sha="$(git ls-remote --tags --refs "$release_git_url" "refs/tags/$desktop_tag" | awk 'NR == 1 {print $1}')"
if [ -z "$cli_sha" ] || [ "$cli_sha" != "$npm_sha" ] || [ "$cli_sha" != "$desktop_sha" ]; then
	echo "::error::release tags are missing or do not identify one immutable commit" >&2
	exit 1
fi
if [ -n "${RELEASE_EXPECTED_SHA:-}" ] && [ "$cli_sha" != "$RELEASE_EXPECTED_SHA" ]; then
	echo "::error::public release identity differs from the verified source SHA" >&2
	exit 1
fi

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/reasonix-release-postflight.XXXXXX")"
cleanup() {
	case "$tmp_dir" in
	*/reasonix-release-postflight.*) rm -rf -- "$tmp_dir" ;;
	*) echo "refusing to clean unexpected postflight directory: $tmp_dir" >&2 ;;
	esac
}
trap cleanup EXIT

# The Desktop updater pointer and the Homebrew cask are product channels. The
# contents API reads the tap's branch head; raw.githubusercontent.com is cached.
verify_pointers() {
	local manifest="$1"
	local cask="$tmp_dir/reasonix.rb"
	local homebrew_version=frozen
	jq -e --arg version "v$version" '
		.version == $version and
		([.platforms[], (.native_packages // {})[], (.downloads // {})[]] |
		 all(.url | type == "string" and startswith("https://dl.reasonix.io/desktop-" + $version + "/")))
	' "$manifest" >/dev/null
	if [ "$frozen" != "true" ]; then
		gh api -H 'Accept: application/vnd.github.raw' \
			repos/esengine/homebrew-reasonix/contents/Casks/reasonix.rb >"$cask"
		homebrew_version="$(sed -nE "s/^[[:space:]]*version ['\"]([^'\"]+)['\"].*/\1/p" "$cask" | head -n 1)"
	fi
	node "$script_dir/release-publication-ledger.mjs" pointers "$version" "$cli_sha" "$operation" \
		"$manifest" "$homebrew_version" "$tmp_dir/pointer-ledger.json"
}

gh release view "$cli_tag" --repo "$repository" --json isDraft,isPrerelease,assets >"$tmp_dir/cli.json"
jq -e '
  .isDraft == false and .isPrerelease == false and
  ([.assets[].name] as $names |
    ["SHA256SUMS", "reasonix-darwin-amd64.tar.gz", "reasonix-darwin-arm64.tar.gz",
     "reasonix-linux-amd64.tar.gz", "reasonix-linux-arm64.tar.gz",
     "reasonix-windows-amd64.zip", "reasonix-windows-arm64.zip"] |
    all(. as $required | $names | index($required)))
' "$tmp_dir/cli.json" >/dev/null

gh release view "$desktop_tag" --repo "$repository" --json isDraft,isPrerelease,assets >"$tmp_dir/desktop.json"
jq -e '
  .isDraft == false and .isPrerelease == false and
  ([.assets[].name] as $names |
    ($names | index("latest.json")) and
    (["Reasonix-darwin-arm64.dmg", "Reasonix-darwin-amd64.dmg",
      "Reasonix-darwin-universal.dmg", "Reasonix-darwin-arm64.zip",
      "Reasonix-darwin-amd64.zip", "Reasonix-linux-amd64.deb",
      "Reasonix-linux-amd64.tar.gz", "Reasonix-windows-amd64-installer.exe",
      "Reasonix-windows-amd64.zip", "Reasonix-windows-arm64-installer.exe"] |
     all(. as $required | ($names | index($required)) and ($names | index($required + ".minisig")))))
' "$tmp_dir/desktop.json" >/dev/null

if [ "${DESKTOP_MANUAL_ONLY:-false}" = "true" ]; then
	bash "$script_dir/manual-desktop-exception.sh" validate "$desktop_tag"
	# Neither updater entry point may serve the manual release: the exception
	# publishes downloads without advancing automatic updates. Assert that
	# invariant rather than one release's prior version.
	gh_latest="$(gh api "repos/$repository/releases/latest" --jq .tag_name)"
	if [ "$gh_latest" = "$desktop_tag" ]; then
		echo "::error::GitHub latest advanced to the manual release $desktop_tag" >&2
		exit 1
	fi
	bash "$script_dir/fetch-stable-release-manifest.sh" "$version" "$tmp_dir/desktop-pointer.json"
	jq -e --arg v "v$version" '.version != $v' "$tmp_dir/desktop-pointer.json" >/dev/null
	gh release view "$desktop_tag" --repo "$repository" --json body --jq .body | grep -F 'manual-download only'
fi

npm_names=(
	"reasonix"
	"@reasonix/cli-darwin-arm64"
	"@reasonix/cli-darwin-x64"
	"@reasonix/cli-linux-arm64"
	"@reasonix/cli-linux-x64"
	"@reasonix/cli-win32-arm64"
	"@reasonix/cli-win32-x64"
)
for attempt in $(seq 1 "$attempts"); do
	rm -rf "$tmp_dir/npm"
	mkdir -p "$tmp_dir/npm"
	visible=true
	for index in "${!npm_names[@]}"; do
		package="${npm_names[$index]}"
		raw="$tmp_dir/npm/$index.raw.json"
		if ! npm view "$package@$version" name version reasonixCandidateSha gitHead dist.integrity "dist-tags.$npm_tag" --json >"$raw" 2>/dev/null; then
			visible=false
			continue
		fi
		jq --arg name "$package" --arg tag "$npm_tag" '
			(."dist-tags.\($tag)" // ."dist-tags"[$tag]) as $tagged |
			{
				name: (.name // $name),
				version,
				reasonixCandidateSha,
				gitHead,
				integrity: (."dist.integrity" // .dist.integrity)
			} + (if $tag == "latest" then {latest: $tagged} else {distTag: $tag, distTagVersion: $tagged} end)
		' "$raw" >"$tmp_dir/npm/$index.json"
		jq -e --arg name "$package" --arg version "$version" --arg sha "$cli_sha" '
			.name == $name and .version == $version and
			((.reasonixCandidateSha == null or .reasonixCandidateSha == $sha) and
			 (.gitHead == null or .gitHead == $sha) and
			 ((.reasonixCandidateSha // .gitHead) == $sha)) and
			(.integrity | type == "string" and length > 0) and
			((.latest // .distTagVersion) | type == "string" and length > 0)
		' "$tmp_dir/npm/$index.json" >/dev/null || {
			echo "::error::npm package identity differs from the release candidate: $package@$version" >&2
			exit 1
		}
	done
	if [ "$visible" = "true" ]; then
		jq -s '.' "$tmp_dir"/npm/[0-9].json >"$tmp_dir/npm.json"
		if node "$script_dir/release-publication-ledger.mjs" core "$version" "$cli_sha" "$operation" \
			"$tmp_dir/cli.json" "$tmp_dir/desktop.json" "$tmp_dir/npm.json" "$tmp_dir/core-ledger.json"; then
			if [ "$verify_pointers" = "true" ]; then
				manifest="$tmp_dir/desktop-pointer.json"
				bash "$script_dir/fetch-stable-release-manifest.sh" "$version" "$manifest"
				owns_pointer="$(node "$script_dir/release-publication-ledger.mjs" pointer-owner "$version" "$operation" "$manifest")"
				if [ "$owns_pointer" = true ]; then
					verify_pointers "$manifest"
					node "$script_dir/release-publication-ledger.mjs" merge "$tmp_dir/core-ledger.json" \
						"$tmp_dir/pointer-ledger.json" "${ledger_output:-$tmp_dir/publication-ledger.json}"
				else
					echo "A verified newer Stable release owns the public pointers; recovered immutable v$version files only."
					[ -z "$ledger_output" ] || cp "$tmp_dir/core-ledger.json" "$ledger_output"
				fi
			elif [ -n "$ledger_output" ]; then
				cp "$tmp_dir/core-ledger.json" "$ledger_output"
			fi
			echo "stable release postflight OK: cli=$cli_tag desktop=$desktop_tag npm-packages=${#npm_names[@]}"
			exit 0
		fi
	fi
	echo "npm publication has not converged (attempt $attempt/$attempts)"
	if [ "$attempt" -lt "$attempts" ]; then sleep "$delay"; fi
done

echo "::error::npm package set or public pointers did not converge for $version" >&2
exit 1

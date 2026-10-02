#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/reasonix-candidate-tags-test.XXXXXX")"
trap 'rm -rf -- "$test_root"' EXIT
git init -q --bare "$test_root/remote.git"
git init -q "$test_root/product"
git -C "$test_root/product" config user.name test
git -C "$test_root/product" config user.email test@example.invalid
git -C "$test_root/product" remote add origin "$test_root/remote.git"
git -C "$test_root/product" commit -qm product --allow-empty
source_sha="$(git -C "$test_root/product" rev-parse HEAD)"
git -C "$test_root/product" commit -qm unrelated --allow-empty
other_sha="$(git -C "$test_root/product" rev-parse HEAD)"

run() { (cd "$test_root/product" && bash "$repo_root/scripts/release-candidate-tags.sh" "$@" "$source_sha"); }
refuse() {
	if run "$@" >"$test_root/refused.log" 2>&1; then
		echo "unsafe tag state unexpectedly accepted: $*" >&2
		exit 1
	fi
}

# Recovery before activation is allowed and performs one atomic creation.
run check recover 1.2.3
run activate recover 1.2.3
run check recover 1.2.3
run activate recover 1.2.3
refuse check publish 1.2.3
test "$(git --git-dir="$test_root/remote.git" for-each-ref --format='%(objectname)' refs/tags | sort -u)" = "$source_sha"

# Partial identity never gets filled in by guessing.
git -C "$test_root/product" push -q origin "$source_sha:refs/tags/v1.2.4"
refuse check recover 1.2.4
refuse activate recover 1.2.4
test "$(git ls-remote --tags "$test_root/remote.git" 'refs/tags/*v1.2.4' | wc -l | tr -d ' ')" = 1

for tag in v1.2.5 npm-v1.2.5 desktop-v1.2.5; do
	git -C "$test_root/product" tag -a "$tag" -m identity "$source_sha"
done
git -C "$test_root/product" push -q origin refs/tags/v1.2.5 refs/tags/npm-v1.2.5 refs/tags/desktop-v1.2.5
run check recover 1.2.5

git -C "$test_root/product" push -q origin "$other_sha:refs/tags/v1.2.6"
refuse activate recover 1.2.6
grep -Fq 'release tag content conflict' "$test_root/refused.log"

run check publish 1.2.7
run activate publish 1.2.7
run check recover 1.2.7

# A server rejecting one member must leave all three absent.
cat >"$test_root/remote.git/hooks/update" <<'HOOK'
#!/usr/bin/env bash
test "$1" != refs/tags/npm-v1.2.8
HOOK
chmod +x "$test_root/remote.git/hooks/update"
refuse activate recover 1.2.8
test -z "$(git ls-remote --tags "$test_root/remote.git" 'refs/tags/*v1.2.8')"
rm "$test_root/remote.git/hooks/update"
run activate recover 1.2.8
echo "candidate tag activation and recovery tests: PASS"

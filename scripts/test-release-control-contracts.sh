#!/usr/bin/env bash
# Offline contracts only. Live public reachability is a separate runner check.
set -euo pipefail
root="${1:-$(git rev-parse --show-toplevel)}"
cd "$root"
node --test scripts/release-candidate.test.mjs scripts/resolve-release-candidate.test.mjs \
	scripts/verify-release-artifact-archive.test.mjs scripts/release-publication-ledger.test.mjs \
	scripts/reconcile-release-publication.test.mjs
bash scripts/release-candidate-tags.test.sh
bash scripts/release-stable.test.sh
echo 'release executable contracts: PASS'

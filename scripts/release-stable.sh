#!/usr/bin/env bash
# Dispatch publication or recovery for a previously sealed release candidate.
# Tag creation belongs to the approved protected workflow, after provenance and
# artifact bytes have been verified.
set -euo pipefail

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ] || [[ ! "$1" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-[0-9a-f]{12}-[0-9a-f]{12}$ ]]; then
	echo "usage: scripts/release-stable.sh CANDIDATE_ID [publish|recover]" >&2
	exit 2
fi

candidate_id="$1"
operation="${2:-publish}"
case "$operation" in publish | recover) ;; *) echo "operation must be publish or recover" >&2; exit 2 ;; esac
repository="${RELEASE_REPOSITORY:-esengine/DeepSeek-Reasonix}"

for command in gh jq; do
	command -v "$command" >/dev/null || { echo "required command is unavailable: $command" >&2; exit 2; }
done

payload="$(jq -cn --arg ref main-v2 --arg candidate_id "$candidate_id" --arg operation "$operation" \
	'{ref: $ref, inputs: {candidate_id: $candidate_id, operation: $operation}}')"
gh api -X POST "repos/$repository/actions/workflows/release-promote.yml/dispatches" --input - <<<"$payload"

echo "Dispatched $operation for $candidate_id on protected main-v2."
echo "The workflow verifies the sealed candidate, then requests one release approval."

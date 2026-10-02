#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
export ACTUAL_CALLER_WORKFLOW_REF='example/reasonix/.github/workflows/release-candidate.yml@refs/heads/main-v2'
export EXPECTED_CALLER_WORKFLOW_REF="$ACTUAL_CALLER_WORKFLOW_REF"
export CALLER_REF=refs/heads/main-v2 CALLER_REF_PROTECTED=true
export CALLER_WORKFLOW_SHA=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
export CALLER_SHA="$CALLER_WORKFLOW_SHA" APPROVED_SHA="$CALLER_WORKFLOW_SHA"
export APPROVED_CLI_TAG=v1.2.3 APPROVED_CHANNEL=stable

for event in push workflow_dispatch; do
	CALLER_EVENT_NAME="$event" bash "$repo_root/scripts/verify-release-authorization.sh"
done
refuse() {
	if env "$@" bash "$repo_root/scripts/verify-release-authorization.sh" >/dev/null 2>&1; then
		echo "unsafe release caller unexpectedly authorized: $*" >&2
		exit 1
	fi
}
for event in pull_request_target pull_request workflow_call; do refuse CALLER_EVENT_NAME="$event"; done
refuse CALLER_EVENT_NAME=push CALLER_REF_PROTECTED=false
refuse CALLER_EVENT_NAME=push CALLER_REF=refs/heads/topic
refuse CALLER_EVENT_NAME=push CALLER_SHA=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
refuse CALLER_EVENT_NAME=push APPROVED_SHA=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
refuse CALLER_EVENT_NAME=push ACTUAL_CALLER_WORKFLOW_REF='example/reasonix/.github/workflows/other.yml@refs/heads/main-v2'
refuse CALLER_EVENT_NAME=push \
	ACTUAL_CALLER_WORKFLOW_REF='example/reasonix/.github/workflows/release-promote.yml@refs/heads/main-v2' \
	EXPECTED_CALLER_WORKFLOW_REF='example/reasonix/.github/workflows/release-promote.yml@refs/heads/main-v2'
echo "protected candidate authorization tests: PASS"

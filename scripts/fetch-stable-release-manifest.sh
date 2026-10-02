#!/usr/bin/env bash
# Public machine endpoint: no credentials, alternate host, or challenge bypass.
set -euo pipefail
test "$#" -eq 2 || { echo 'usage: fetch-stable-release-manifest.sh VERSION OUTPUT' >&2; exit 2; }
version="$1"
[[ "$version" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || { echo 'Stable manifest probe requires a stable version' >&2; exit 2; }
output="$2"
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# Use the shipped updater's Go HTTP stack. On Actions runners, curl and Node
# can receive a Cloudflare challenge for the same public URL and User-Agent.
go run "$script_dir/release-manifest-fetch/main.go" "$version" "$output"

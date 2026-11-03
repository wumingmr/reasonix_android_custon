#!/usr/bin/env bash
# Shared tag/version rules for local publishing and GitHub verification.
termux_release_identity() {
  local upstream="$1" requested="${2:-}" tag_base
  [[ "$upstream" =~ ^v1\.[0-9]+\.[0-9]+$ ]] || {
    echo "Unsupported upstream version: $upstream" >&2; return 1;
  }
  TERMUX_RELEASE_TAG="${requested:-termux-$upstream}"
  [[ "$TERMUX_RELEASE_TAG" =~ ^termux-v1\.[0-9]+\.[0-9]+(-r[1-9][0-9]*)?$ ]] || {
    echo "Invalid Termux release tag: $TERMUX_RELEASE_TAG" >&2; return 1;
  }
  tag_base="${TERMUX_RELEASE_TAG#termux-}"
  tag_base="${tag_base%%-r*}"
  [ "$tag_base" = "$upstream" ] || {
    echo "Release tag $TERMUX_RELEASE_TAG does not track $upstream" >&2; return 1;
  }
  TERMUX_RELEASE_ASSET="reasonix-android-arm64-${TERMUX_RELEASE_TAG#termux-}"
}

termux_verify_binary_version() {
  local version="$1" upstream="$2"
  [[ "$version" =~ ^1\.[0-9]+\.[0-9]+-termux\.[[:alnum:].+-]+$ ]] && \
    [ "${version%%-termux*}" = "${upstream#v}" ] || {
      echo "Binary version ${version:-missing} does not track $upstream" >&2; return 1;
    }
}

#!/usr/bin/env python3
"""Auto-merge Makefile conflicts for update-reasonix.sh.

When cherry-picking the termux-patch branch onto a newer upstream tag, the
Makefile often conflicts because upstream changed .PHONY / removed wails.
Strategy: take the upstream (HEAD) version as base, then re-apply only the
android-related bits that the patch adds:
  - ANDROID_ARCH ?= arm64  (after GOEXE line, if missing)
  - android: target        (after build target, if missing)
  - 'android' in .PHONY line (add if missing)
Drop everything wails-related (upstream removed it).
"""
import re
import sys


def merge(base: str, patch: str) -> str:
    # 1. Ensure ANDROID_ARCH variable after GOEXE line
    if 'ANDROID_ARCH ?= arm64' not in base:
        patch_arch = re.search(r'^ANDROID_ARCH \?= arm64$', patch, re.M)
        if patch_arch:
            base = re.sub(
                r'^(GOEXE := .*)$',
                r'\1\nANDROID_ARCH ?= arm64',
                base,
                count=1,
                flags=re.M,
            )

    # 2. Ensure android: target after build target
    if re.search(r'^android:\n', base, re.M) is None:
        patch_android = re.search(
            r'^android:\n(\t[^\n]*\n?)+', patch, re.M
        )
        if patch_android:
            base = re.sub(
                r'^(\tCGO_ENABLED=0 go build .*cmd/reasonix-plugin-example.*)$',
                r'\1\n\n' + patch_android.group(0).rstrip('\n'),
                base,
                count=1,
                flags=re.M,
            )

    # 3. Ensure 'android' in .PHONY (patch line, minus wails-install)
    def phony_repl(m):
        words = m.group(1).split()
        if 'android' not in words:
            words.insert(1, 'android')
        return '.PHONY: ' + ' '.join(words)

    base = re.sub(
        r'^\.PHONY: (.*)$',
        phony_repl,
        base,
        count=1,
        flags=re.M,
    )
    return base


def main():
    base_path, patch_path, out_path = sys.argv[1], sys.argv[2], sys.argv[3]
    with open(base_path) as f:
        base = f.read()
    with open(patch_path) as f:
        patch = f.read()
    merged = merge(base, patch)
    with open(out_path, 'w') as f:
        f.write(merged)


if __name__ == '__main__':
    main()

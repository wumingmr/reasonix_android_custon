#!/usr/bin/env bash
# Verify the Windows portable release unit (versioned-v1) before packaging.
# This must run on the Windows staging directory: NTFS treats file names
# case-insensitively, so Reasonix.exe and reasonix.exe silently overwrite each
# other even though a source-level packaging test sees two different strings.
set -euo pipefail

staging="${1:?usage: verify-windows-portable.sh STAGING_DIR [canonical|legacy-dual] [PAYLOAD_LAUNCHER]}"
layout="${2:-canonical}"
payload_launcher="${3:-}"
case "$layout" in
	canonical|legacy-dual) ;;
	*) echo "Unknown Windows portable layout: $layout" >&2; exit 1 ;;
esac
[ -d "$staging" ] || { echo "Windows portable staging directory is missing: $staging" >&2; exit 1; }

# Root entry points for versioned-v1 (no Guard, no flat desktop/helper).
required_root=(
	"Reasonix.exe"
	"reasonix-cli.exe"
	"current.json"
)
if [ "$layout" = "legacy-dual" ]; then
	required_root+=("reasonix-launcher.exe")
fi

for expected in "${required_root[@]}"; do
	[ -f "$staging/$expected" ] && [ ! -L "$staging/$expected" ] || {
		echo "Windows portable root entry is missing: $expected" >&2
		exit 1
	}
done

# Exactly one active version directory under versions/v*.
version_dirs=()
if [ -d "$staging/versions" ]; then
	while IFS= read -r -d '' d; do
		version_dirs+=("$d")
	done < <(find "$staging/versions" -mindepth 1 -maxdepth 1 -type d -name 'v*' -print0 2>/dev/null || true)
fi
if [ "${#version_dirs[@]}" -ne 1 ]; then
	echo "Windows portable must contain exactly one active version directory" >&2
	exit 1
fi

# current.json must point at a real version directory.
active_dir=$(node -e '
const fs = require("node:fs");
const p = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
if (p.schemaVersion !== 1 || !/^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?$/.test(p.activeVersion) ||
    p.activeDir !== `versions/${p.activeVersion}` ||
    Object.keys(p).some(k => !["schemaVersion", "activeVersion", "activeDir"].includes(k))) {
  throw new Error("Invalid portable current.json");
}
console.log(p.activeDir);
' "$staging/current.json")
[ -n "$active_dir" ] || {
	echo "Windows portable current.json activeDir is empty or unreadable" >&2
	exit 1
}
version_path="$staging/$active_dir"
[ -d "$version_path" ] || {
	echo "Windows portable activeDir does not exist: $active_dir" >&2
	exit 1
}
for name in reasonix-desktop.exe reasonix-cli.exe reasonix-update-helper.exe; do
	[ -f "$version_path/$name" ] || {
		echo "Windows portable version member is missing: $active_dir/$name" >&2
		exit 1
	}
done

# The Electron bundle is the app/ tree member of the active version.
for name in "app/Reasonix.exe" "app/resources/app.asar" "app/resources/build.json" "app/resources/app/index.html" "app/resources/bin/reasonix-cli-launcher.exe"; do
	[ -f "$version_path/$name" ] || {
		echo "Windows portable app tree member is missing: $active_dir/$name" >&2
		exit 1
	}
done

# Guard must not persist in a normal portable layout.
if [ -e "$staging/reasonix-guard.exe" ] || [ -e "$staging/reasonix-desktop.exe" ]; then
	echo "Windows portable must not ship flat reasonix-guard.exe or reasonix-desktop.exe at InstallRoot" >&2
	exit 1
fi

# Historical packages must contain identical launcher entries. New artifacts
# must not regain the compatibility entry merely because it exists in staging.
if [ "$layout" = "legacy-dual" ]; then
	cmp -s "$staging/Reasonix.exe" "$staging/reasonix-launcher.exe" || {
		echo "Reasonix.exe differs from the legacy GUI entry" >&2; exit 1
	}
fi
if [ -n "$payload_launcher" ]; then
	cmp -s "$staging/Reasonix.exe" "$payload_launcher" || {
		echo "Reasonix.exe does not match the payload launcher" >&2; exit 1
	}
fi
if cmp -s "$staging/Reasonix.exe" "$staging/reasonix-cli.exe"; then
	echo "Reasonix.exe was overwritten by the CLI sidecar" >&2
	exit 1
fi
cmp -s "$staging/reasonix-cli.exe" "$version_path/app/resources/bin/reasonix-cli-launcher.exe" || {
	echo "reasonix-cli.exe is not the packaged CLI forwarding entry" >&2
	exit 1
}
if cmp -s "$staging/reasonix-cli.exe" "$version_path/reasonix-cli.exe"; then
	echo "Windows portable duplicated the full CLI at InstallRoot" >&2
	exit 1
fi

# Case-insensitive collision check among root exes.
actual=()
while IFS= read -r -d '' path; do
	[ -f "$path" ] && [ ! -L "$path" ] || { echo "Invalid Windows root entry: $path" >&2; exit 1; }
	name="${path##*/}"
	case "$name" in
		Reasonix.exe|reasonix-cli.exe) ;;
		reasonix-launcher.exe) [ "$layout" = "legacy-dual" ] || { echo "Unexpected legacy launcher" >&2; exit 1; } ;;
		*) echo "Unexpected Windows root executable: $name" >&2; exit 1 ;;
	esac
	folded=$(printf '%s' "$name" | tr '[:upper:]' '[:lower:]')
	if [ "${#actual[@]}" -gt 0 ]; then
		for previous in "${actual[@]}"; do
			previous_folded=$(printf '%s' "$previous" | tr '[:upper:]' '[:lower:]')
			if [ "$folded" = "$previous_folded" ]; then
				echo "Windows portable names collide case-insensitively: $previous and $name" >&2
				exit 1
			fi
		done
	fi
	actual+=("$name")
done < <(find "$staging" -maxdepth 1 \( -iname '*.exe' -o -iname '*.dll' \) -print0)

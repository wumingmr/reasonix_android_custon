#!/usr/bin/env bash
set -euo pipefail

arch="${1:?usage: finalize-windows-signed-candidate.sh ARCH SIGNING_WORK PAYLOAD DIST BUNDLE VERSION}"
signing_work="${2:?missing signing work directory}"
signed_payload="${3:?missing signed payload directory}"
dist="${4:?missing output dist directory}"
bundle="${5:?missing output bundle directory}"
version="${6:?missing release version}"

case "$arch" in
amd64 | arm64) ;;
*) echo "unsupported Windows architecture: $arch" >&2; exit 1 ;;
esac

product_root="$(pwd)"
control_root="$(cd "$(dirname "$0")/.." && pwd)"
case "$signing_work" in /*) ;; *) signing_work="$product_root/$signing_work" ;; esac
case "$signed_payload" in /*) ;; *) signed_payload="$product_root/$signed_payload" ;; esac
case "$dist" in /*) ;; *) dist="$product_root/$dist" ;; esac
case "$bundle" in /*) ;; *) bundle="$product_root/$bundle" ;; esac
for variable in CERTUM_KEY_ID MINISIGN_PRIVATE_KEY MINISIGN_PASSWORD RELEASE_SOURCE_SHA RELEASE_CONTROL_SHA RELEASE_TAG RELEASE_VERSION RELEASE_CHANNEL RELEASE_SIGNING_FINGERPRINT RELEASE_ARTIFACT_PREFIX; do
	[ -n "${!variable:-}" ] || { echo "missing $variable" >&2; exit 1; }
done

# FINALIZE_PHASE splits the run so the Certum steps stay in one session while
# the two architectures compress their installers at the same time:
# sign (Certum), package (no credentials), seal (Certum). Unset runs all three.
phase="${FINALIZE_PHASE:-all}"
case "$phase" in
all | sign | package | seal) ;;
*) echo "unsupported FINALIZE_PHASE: $phase" >&2; exit 1 ;;
esac
run_phase() { [ "$phase" = all ] || [ "$phase" = "$1" ]; }

if run_phase sign; then
	pwsh -NoProfile -File "$control_root/scripts/sign-certum.ps1" -PayloadDirectory "$signed_payload"
fi
if ! run_phase package && ! run_phase seal; then
	exit 0
fi

installer="$dist/Reasonix-windows-$arch-installer.exe"
portable="$dist/Reasonix-windows-$arch.zip"
if run_phase package; then
	# Keep the immutable checkout's NSIS template and icon. The signing handoff
	# carries generated identity and payload files, not these committed inputs.
	rm -rf "$dist" "$bundle"
	mkdir -p "$product_root/desktop/build/windows/installer"
	cp "$signing_work/desktop/build/windows/installer/reasonix_project.nsh" \
		"$product_root/desktop/build/windows/installer/"
	(
		cd "$product_root/desktop"
		go run ./cmd/sign windows-payload "$signed_payload" "$version"
		go run ./cmd/sign sign "$signed_payload/reasonix-payload.json"
		go run ./cmd/sign verify "$signed_payload/reasonix-payload.json"
	)

	REASONIX_REQUIRE_PAYLOAD_MANIFEST=1 \
		"$product_root/scripts/package-windows-desktop.sh" "$arch" "$signed_payload"
	mv "$product_root/dist" "$dist"
fi
run_phase seal || exit 0

pwsh -NoProfile -File "$control_root/scripts/sign-certum.ps1" -FilePath "$installer"

portable_layout="legacy-dual"
if [ -f "$product_root/desktop/packaging/windows-portable-layout.txt" ]; then
	portable_layout="$(tr -d '\r\n' < "$product_root/desktop/packaging/windows-portable-layout.txt")"
fi
pwsh -NoProfile -File "$control_root/scripts/verify-windows-authenticode.ps1" \
	-PayloadDirectory "$signed_payload" -InstallerPath "$installer" \
	-PortableArchivePath "$portable" -ExpectedThumbprint "$CERTUM_KEY_ID" \
	-RequireTrusted -PortableLayout "$portable_layout"

node "$product_root/desktop/packaging/size-report.mjs" \
	--platform "windows/$arch" --version "$version" \
	--bundle "$signed_payload" --dist "$dist" \
	--output "$product_root/desktop/build/reports/windows-$arch"
(
	cd "$product_root/desktop"
	go run ./cmd/sign sign "$dist"/*
)
node "$control_root/scripts/desktop-release-artifacts.mjs" pack "$dist" "$bundle" "windows-$arch"

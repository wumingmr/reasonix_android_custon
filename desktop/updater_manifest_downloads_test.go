package main

import "testing"

func TestDesktopManifestArchitectureDownloads(t *testing.T) {
	t.Run("historical two-download manifests remain upgradeable", func(t *testing.T) {
		stable := validDesktopManifest(t, "stable", "v1.17.21")
		delete(stable.Downloads, "Reasonix-darwin-arm64.dmg")
		delete(stable.Downloads, "Reasonix-darwin-amd64.dmg")
		if err := validateDesktopManifest("stable", &stable); err != nil {
			t.Fatalf("historical Stable manifest: %v", err)
		}
	})
	t.Run("partial architecture DMGs fail closed", func(t *testing.T) {
		manifest := validDesktopManifest(t, "stable", "v1.39.0")
		delete(manifest.Downloads, "Reasonix-darwin-amd64.dmg")
		if err := validateDesktopManifest("stable", &manifest); err == nil {
			t.Fatal("manifest with one architecture DMG passed validation")
		}
	})
}

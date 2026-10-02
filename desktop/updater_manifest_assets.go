package main

type requiredDesktopAsset struct {
	group    string
	key      string
	filename string
}

var (
	requiredDesktopUpdaterAssets = []requiredDesktopAsset{
		{group: "platforms", key: "darwin-arm64", filename: "Reasonix-darwin-arm64.zip"},
		{group: "platforms", key: "darwin-amd64", filename: "Reasonix-darwin-amd64.zip"},
		{group: "platforms", key: "windows-amd64", filename: "Reasonix-windows-amd64-installer.exe"},
		{group: "platforms", key: "windows-arm64", filename: "Reasonix-windows-arm64-installer.exe"},
		{group: "platforms", key: "linux-amd64", filename: "Reasonix-linux-amd64.tar.gz"},
		{group: "native_packages", key: "linux-amd64", filename: "Reasonix-linux-amd64.deb"},
	}
	legacyDesktopDownloadAssets = []requiredDesktopAsset{
		{group: "downloads", key: "Reasonix-darwin-universal.dmg", filename: "Reasonix-darwin-universal.dmg"},
		{group: "downloads", key: "Reasonix-windows-amd64.zip", filename: "Reasonix-windows-amd64.zip"},
	}
	requiredDesktopDownloadAssets = append(append([]requiredDesktopAsset(nil), legacyDesktopDownloadAssets...),
		requiredDesktopAsset{group: "downloads", key: "Reasonix-darwin-arm64.dmg", filename: "Reasonix-darwin-arm64.dmg"},
		requiredDesktopAsset{group: "downloads", key: "Reasonix-darwin-amd64.dmg", filename: "Reasonix-darwin-amd64.dmg"},
	)
)

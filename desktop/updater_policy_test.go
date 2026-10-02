package main

import (
	"errors"
	"net/http"
	"testing"

	"reasonix/internal/config"
)

func setDesktopBuildIdentityForTest(t *testing.T, buildVersion, buildChannel string) {
	t.Helper()
	oldVersion, oldChannel := version, channel
	version, channel = buildVersion, buildChannel
	t.Cleanup(func() {
		version, channel = oldVersion, oldChannel
	})
}

func TestDesktopUpdaterEnabledRequiresExactStableReleaseBuild(t *testing.T) {
	tests := []struct {
		name    string
		version string
		channel string
		want    bool
	}{
		{name: "stable release", version: "v1.2.3", channel: "stable", want: true},
		{name: "stable prerelease", version: "v1.2.3-rc.1", channel: "stable"},
		{name: "stable test version", version: "v0.0.0-test.20260916.abc1234", channel: "stable"},
		{name: "canary", version: "v1.2.3", channel: "canary"},
		{name: "preview", version: "v1.2.3", channel: "preview"},
		{name: "test", version: "v1.2.3", channel: "test"},
		{name: "dev channel", version: "v1.2.3", channel: "dev"},
		{name: "dev version", version: "dev", channel: "stable"},
		{name: "channel must be exact", version: "v1.2.3", channel: "Stable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setDesktopBuildIdentityForTest(t, tt.version, tt.channel)
			if got := desktopUpdaterEnabled(); got != tt.want {
				t.Fatalf("desktopUpdaterEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDisabledDesktopUpdaterBoundariesHaveNoSideEffects(t *testing.T) {
	setDesktopBuildIdentityForTest(t, "v0.0.0-test.20260916.abc1234", "canary")

	originalClient := updaterHTTPClient
	originalIPv4 := updaterHTTPClientIPv4
	originalPending := pendingUpdateExistsForInstall
	t.Cleanup(func() {
		updaterHTTPClient = originalClient
		updaterHTTPClientIPv4 = originalIPv4
		pendingUpdateExistsForInstall = originalPending
	})

	clientCalls := 0
	updaterHTTPClient = func() (*http.Client, error) {
		clientCalls++
		return nil, errors.New("unexpected updater HTTP client")
	}
	updaterHTTPClientIPv4 = func() (*http.Client, error) {
		clientCalls++
		return nil, errors.New("unexpected updater IPv4 client")
	}
	pendingCalls := 0
	pendingUpdateExistsForInstall = func() bool {
		pendingCalls++
		return true
	}

	app, host := newRecordingHostApp(t)
	info, err := app.CheckUpdate("stable")
	if err != nil || info == nil || info.Available || info.Current != version {
		t.Fatalf("disabled CheckUpdate() = (%+v, %v), want unavailable current build", info, err)
	}
	if err := app.ApplyUpdateRequest("stable", "v1.2.3", "disabled-apply"); !errors.Is(err, errUpdateDisabled) {
		t.Fatalf("disabled ApplyUpdateRequest() error = %v, want %v", err, errUpdateDisabled)
	}
	app.OpenDownloadPage()
	if err := app.AbandonPendingUpdate(); !errors.Is(err, errUpdateDisabled) {
		t.Fatalf("disabled AbandonPendingUpdate() error = %v, want %v", err, errUpdateDisabled)
	}
	if clientCalls != 0 || pendingCalls != 0 {
		t.Fatalf("disabled updater side effects: HTTP=%d pending=%d", clientCalls, pendingCalls)
	}
	assertHostCalls(t, host)
}

func TestDesktopUpdaterCapabilityAndConfigFailureDefaults(t *testing.T) {
	setDesktopBuildIdentityForTest(t, "v1.2.3", "stable")

	failedStartup := desktopStartupSettingsFromConfig(nil)
	if failedStartup.CheckUpdates || !failedStartup.UpdaterEnabled {
		t.Fatalf("failed startup settings = check:%v updater:%v, want false/true", failedStartup.CheckUpdates, failedStartup.UpdaterEnabled)
	}
	failedFull := NewApp().defaultSettingsView()
	if failedFull.CheckUpdates || !failedFull.UpdaterEnabled {
		t.Fatalf("failed full settings = check:%v updater:%v, want false/true", failedFull.CheckUpdates, failedFull.UpdaterEnabled)
	}
	normal := desktopStartupSettingsFromConfig(config.Default())
	if !normal.CheckUpdates || !normal.UpdaterEnabled {
		t.Fatalf("normal stable settings = check:%v updater:%v, want true/true", normal.CheckUpdates, normal.UpdaterEnabled)
	}

	channel = "canary"
	testBuild := desktopStartupSettingsFromConfig(config.Default())
	if !testBuild.CheckUpdates || testBuild.UpdaterEnabled {
		t.Fatalf("canary settings = check:%v updater:%v, want preserved preference with disabled capability", testBuild.CheckUpdates, testBuild.UpdaterEnabled)
	}
}

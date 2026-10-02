package boot

import (
	"context"
	"io"
	"testing"

	"reasonix/internal/skill/skillwatch"
)

// A package test binary must never spawn a watcher helper: one per test would
// leave stray reasonix-desktop.exe processes behind. The host constructor is
// what the desktop calls, so it has to apply the same gate as Build.
func TestHostSkillWatchServiceIsDisabledInTestBinaries(t *testing.T) {
	if !watchSkillsEnabled() {
		if service := NewHostSkillWatchService(io.Discard); service != nil {
			_ = service.Close()
			t.Fatal("host skill watch service was created in a package test binary")
		}
		return
	}
	// A production-named binary (an installed desktop service) does own one.
	service := NewHostSkillWatchService(io.Discard)
	if service == nil {
		t.Fatal("host skill watch service is nil in a production binary")
	}
	if err := service.Close(); err != nil {
		t.Fatalf("close host skill watch service: %v", err)
	}
}

// subscribeScanOnly adds one subscription so the counters can show whether a
// close happened. ScanOnly keeps this process-free, which a real helper would
// not: on Windows the helper is os.Executable(), and here that is this test
// binary — the child would re-run the suite.
func subscribeScanOnly(t *testing.T, service *skillwatch.Service) {
	t.Helper()
	sub := service.Subscribe(t.TempDir(), 1,
		func(context.Context, string, int) ([]string, bool) { return nil, true },
		func(context.Context, string, int) ([32]byte, int, bool) { return [32]byte{}, 0, true },
		func(string) {})
	t.Cleanup(sub.Release)
}

// A build must not close a caller-owned service: every other controller on the
// host is still subscribed to it, and closing it tears down the helper they all
// watch through.
func TestCloseSkillsWithWatcherLeavesCallerOwnedServiceOpen(t *testing.T) {
	host := skillwatch.NewService(skillwatch.Options{ScanOnly: true, Stderr: io.Discard})
	subscribeScanOnly(t, host)
	if host.Diagnostics().LogicalSubscriptions != 1 {
		t.Fatal("fixture: the subscription did not register")
	}
	ptr := host
	closeSkillsWithWatcher(nil, nil, &host, true)
	if host != nil {
		t.Fatal("closeSkillsWithWatcher must clear the caller's pointer")
	}
	if ptr.Diagnostics().LogicalSubscriptions != 1 {
		t.Fatal("a caller-owned service must outlive the build that subscribed to it")
	}
	if err := ptr.Close(); err != nil {
		t.Fatalf("close caller-owned service: %v", err)
	}
}

// The mirror case: a service the build created is closed with the build.
func TestCloseSkillsWithWatcherClosesABuildOwnedService(t *testing.T) {
	owned := skillwatch.NewService(skillwatch.Options{ScanOnly: true, Stderr: io.Discard})
	subscribeScanOnly(t, owned)
	if owned.Diagnostics().LogicalSubscriptions != 1 {
		t.Fatal("fixture: the subscription did not register")
	}
	ptr := owned
	closeSkillsWithWatcher(nil, nil, &owned, false)
	if ptr.Diagnostics().LogicalSubscriptions != 0 {
		t.Fatal("a build-owned service must be closed with the build")
	}
}

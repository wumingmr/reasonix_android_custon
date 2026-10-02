package main

import (
	"context"
	"io"
	"testing"

	"reasonix/internal/skill/skillwatch"
)

// The host owns one watcher for its whole lifetime: a second service would be a
// second helper process, and a replacement would strand the subscriptions the
// first one holds. An existing service must be reused, never re-created.
func TestSharedSkillWatchServiceIsReusedPerApp(t *testing.T) {
	app := NewApp()
	seeded := &skillwatch.Service{}
	app.skillWatch = seeded
	for range 3 {
		if got := app.sharedSkillWatchService(); got != seeded {
			t.Fatal("an existing host skill watch service must be reused, not replaced")
		}
	}
}

// The desktop must apply the same production-binary gate as boot. A real
// service created here would spawn a helper through os.Executable(), and in a
// package test binary that helper is this test binary — it would re-run the
// suite in a child process.
func TestSharedSkillWatchServiceIsNilInTestBinaries(t *testing.T) {
	if service := NewApp().sharedSkillWatchService(); service != nil {
		_ = service.Close()
		t.Fatal("a package test binary must not create a skill watch service")
	}
}

// The host watcher is closed only here: boot.Build deliberately leaves a
// caller-owned service alone, so a missed close would leave the helper process
// behind after exit. ScanOnly keeps this process-free.
func TestCloseSharedSkillWatchServiceClosesTheWatcher(t *testing.T) {
	app := NewApp()
	service := skillwatch.NewService(skillwatch.Options{ScanOnly: true, Stderr: io.Discard})
	sub := service.Subscribe(t.TempDir(), 1,
		func(context.Context, string, int) ([]string, bool) { return nil, true },
		func(context.Context, string, int) ([32]byte, int, bool) { return [32]byte{}, 0, true },
		func(string) {})
	t.Cleanup(sub.Release)
	if service.Diagnostics().LogicalSubscriptions != 1 {
		t.Fatal("fixture: the subscription did not register")
	}

	app.skillWatch = service
	app.closeSharedSkillWatchService()
	if service.Diagnostics().LogicalSubscriptions != 0 {
		t.Fatal("the host watcher must be closed")
	}
}

// A rebuild that races shutdown must not re-create the watcher: nothing runs
// the shutdown step again, so a fresh service would leave its helper behind.
func TestSharedSkillWatchServiceIsNotRecreatedAfterClose(t *testing.T) {
	app := NewApp()
	service := skillwatch.NewService(skillwatch.Options{ScanOnly: true, Stderr: io.Discard})
	app.skillWatch = service
	app.closeSharedSkillWatchService()
	if got := app.sharedSkillWatchService(); got != service {
		t.Fatal("a build after shutdown must get the closed host service, not a new one")
	}
	sub := service.Subscribe(t.TempDir(), 1,
		func(context.Context, string, int) ([]string, bool) { return nil, true },
		func(context.Context, string, int) ([32]byte, int, bool) { return [32]byte{}, 0, true },
		func(string) {})
	t.Cleanup(sub.Release)
	if service.Diagnostics().LogicalSubscriptions != 0 {
		t.Fatal("a closed host service must hand out dead subscriptions")
	}
	app.closeSharedSkillWatchService()
}

// shutdownStatus carries no step names, but the coordinator records them in
// finished, which a package test can read. This fails if the step is dropped or
// renamed, which is the wiring half of the shutdown path.
func TestShutdownRunsTheSkillWatchStep(t *testing.T) {
	app := NewApp()
	app.shutdown(context.Background())
	state := app.shutdownState()
	state.mu.Lock()
	ran := state.finished["skill-watch-service"]
	state.mu.Unlock()
	if !ran {
		t.Fatal("shutdown did not run the skill-watch step")
	}
}

package main

import (
	"path/filepath"
	"sync"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/tool"
)

type delayedContextController struct {
	control.SessionAPI
	once             sync.Once
	entered, release chan struct{}
}

func TestContextUsageForTabReadsRotatedSessionWithoutMutatingTelemetry(t *testing.T) {
	dir := t.TempDir()
	rotated, stale := filepath.Join(dir, "rotated.jsonl"), filepath.Join(dir, "stale.jsonl")
	ag := agent.New(usageProvider{}, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	tab := &WorkspaceTab{ID: "tab", Ctrl: newFixtureController(t, control.Options{
		Executor: ag, Sink: event.Discard, SessionDir: dir, SessionPath: rotated,
	})}
	// A legacy /new can rotate before the event writer re-keys telemetry.
	tab.syncTelemetryToSession(stale)
	tab.recordUsage(costedUsageEvent())
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}}
	info := app.ContextUsageForTab("tab")
	if info.SessionCost != 0 || info.SessionTokens != 0 {
		t.Fatalf("rotated session read inherited previous counters: %+v", info)
	}
	if got := tab.telemetrySnapshot().Usage.RequestCount; got != 1 {
		t.Fatalf("read changed live telemetry: %d, want original 1", got)
	}
	// The event/lifecycle writer owns the re-key even when no overview is open.
	tab.syncTelemetryToSession(rotated)
	if got := tab.telemetrySnapshot().Usage.RequestCount; got != 0 {
		t.Fatalf("writer did not reset rotated session telemetry: %d", got)
	}
}

func (c *delayedContextController) SessionPath() string {
	path := c.SessionAPI.SessionPath()
	c.once.Do(func() { close(c.entered); <-c.release })
	return path
}

func TestContextReadsCannotRestorePreviousSessionTelemetry(t *testing.T) {
	for _, panel := range []bool{false, true} {
		for _, replace := range []bool{false, true} {
			name := "usage"
			if panel {
				name = "panel"
			}
			if replace {
				name += "/replacement"
			} else {
				name += "/rotation"
			}
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				oldPath, newPath := filepath.Join(dir, "old.jsonl"), filepath.Join(dir, "new.jsonl")
				makeController := func(path string) *control.Controller {
					ag := agent.New(usageProvider{}, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
					return newFixtureController(t, control.Options{Executor: ag, Sink: event.Discard, SessionDir: dir, SessionPath: path})
				}
				original := makeController(oldPath)
				old := &delayedContextController{SessionAPI: original, entered: make(chan struct{}), release: make(chan struct{})}
				tab := &WorkspaceTab{ID: "tab", Ctrl: old}
				tab.syncTelemetryToSession(oldPath)
				tab.recordUsage(costedUsageEvent())
				if err := saveTelemetry(oldPath+".telemetry.json", tab.telemetrySnapshot()); err != nil {
					t.Fatal(err)
				}
				app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}}
				done := make(chan int, 1)
				go func() {
					if panel {
						done <- app.ContextPanel("tab").TotalTokens
					} else {
						done <- app.ContextUsageForTab("tab").SessionTokens
					}
				}()
				<-old.entered
				if replace {
					next := makeController(newPath)
					app.mu.Lock()
					tab.Ctrl = next
					tab.SessionGeneration++
					app.mu.Unlock()
				} else {
					original.SetSessionPath(newPath)
				}
				tab.resetTelemetry(newPath)
				close(old.release)
				if got := <-done; got != 0 {
					t.Fatalf("stale response contains %d previous-session tokens", got)
				}
				if got := tab.telemetrySnapshot().Usage; got.TotalTokens != 0 || got.SessionCost != 0 || got.RequestCount != 0 {
					t.Fatalf("old read overwrote new telemetry: %+v", got)
				}
				tab.telemMu.Lock()
				key := tab.telemetrySessionKey
				tab.telemMu.Unlock()
				if key != sessionRuntimeKey(newPath) {
					t.Fatalf("telemetry owner = %q", key)
				}
			})
		}
	}
}

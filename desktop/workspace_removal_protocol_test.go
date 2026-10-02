package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

// Removing a project can select a surviving historical JSONL tab. Opening a
// canonical session must replace that native runtime, not require the old
// runtime to already use the destination session protocol.
func TestOpenCanonicalSessionAfterWorkspaceRemovalFromNativeSurface(t *testing.T) {
	for _, removal := range []string{"none", "current-project", "other-project", "cancelled-replacement"} {
		t.Run(removal, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			configureSwitchableDefaultModels(t)
			app := NewApp()
			app.ctx = t.Context()
			app.readyHook = func() {}
			installNoopRuntimeEvents(app)
			t.Cleanup(func() { app.shutdown(context.Background()) })

			root := canonicalRuntimeRoot(t.TempDir())
			if err := addProject(root, "Historical"); err != nil {
				t.Fatal(err)
			}
			dir := desktopSessionDir(root)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "historical.jsonl")
			loaded := agent.NewSession("system")
			loaded.Add(provider.Message{Role: provider.RoleUser, Content: "preserved historical message"})
			if err := loaded.Save(path); err != nil {
				t.Fatal(err)
			}
			source := &SessionSourceRef{HostID: localDesktopHostID, Path: path, HeadID: agent.SessionMainHead}
			meta, err := app.OpenTopicSession("project", root, "historical", nativeSessionSourceRoute(source))
			if err != nil {
				t.Fatal(err)
			}
			native := waitForTabReady(t, app, meta.ID)
			ctrl, ok := native.Ctrl.(*control.Controller)
			if !ok || !ctrl.NativeLegacySession() || ctrl.UsesExclusiveSession() {
				t.Fatal("fixture did not open a native historical JSONL runtime")
			}
			otherRoot := canonicalRuntimeRoot(t.TempDir())
			service := app.desktopSessionService("")
			target, err := service.Create(t.Context(), session.CreateOptions{SessionID: "remaining-session", CWD: otherRoot, Origin: session.SessionOriginNew})
			if err != nil {
				t.Fatal(err)
			}
			ref := target.Ref()
			if _, err := app.attachDesktopSession(t.Context(), "project", otherRoot, ref); err != nil {
				t.Fatal(err)
			}
			if err := service.Close(t.Context(), ref); err != nil {
				t.Fatal(err)
			}

			if removal == "current-project" || removal == "other-project" {
				removedRoot := canonicalRuntimeRoot(t.TempDir())
				if err := addProject(removedRoot, filepath.Base(removedRoot)); err != nil {
					t.Fatal(err)
				}
				if _, err := app.ensureDesktopWorkspace(t.Context(), "project", removedRoot); err != nil {
					t.Fatal(err)
				}
				app.mu.Lock()
				removed := app.createTabEntry("project", removedRoot, "")
				app.tabs[removed.ID] = removed
				app.tabOrder = append(app.tabOrder, removed.ID)
				if removal == "current-project" {
					app.activeTabID = removed.ID
				}
				app.mu.Unlock()
				if err := app.RemoveWorkspace(removedRoot); err != nil {
					t.Fatal(err)
				}
			}

			if removal == "cancelled-replacement" {
				history := ctrl.History()
				built := false
				app.sessionOpenBuildHook = func(context.Context) {
					built = true
					app.cancelSessionNavigation()
				}
				_, err := app.OpenSession(ref)
				if !built || !errors.Is(err, context.Canceled) {
					t.Fatalf("expected cancellation during destination build, built=%v err=%v", built, err)
				}
				if app.controllerForTab(native) != ctrl || !reflect.DeepEqual(ctrl.History(), history) {
					t.Fatal("failed replacement changed the original controller or history")
				}
				select {
				case <-ctrl.Closed():
					t.Fatal("failed replacement closed the native source controller")
				default:
				}
				return
			}
			if _, err := app.OpenSession(ref); err != nil {
				t.Fatalf("open remaining project's canonical session from native surface: %v", err)
			}
			app.mu.RLock()
			active := app.activeTabLocked()
			app.mu.RUnlock()
			if active == nil || active.SessionID != ref.SessionID || !sameDesktopPath(active.WorkspaceRoot, otherRoot) {
				t.Fatal("opening did not commit the selected session and workspace")
			}
			preserved, err := agent.LoadSession(path)
			if err != nil {
				t.Fatalf("navigation lost historical source: %v", err)
			}
			found := false
			for _, message := range preserved.Snapshot() {
				found = found || message.Content == "preserved historical message"
			}
			if !found {
				t.Fatal("navigation lost the historical conversation")
			}
		})
	}
}

func TestCanonicalOpenStillRejectsUnsupportedRuntime(t *testing.T) {
	if _, err := canonicalOpenIdentity(&stubSessionAPI{}); err == nil {
		t.Fatal("an unsupported runtime passed the identity protocol check")
	}
}

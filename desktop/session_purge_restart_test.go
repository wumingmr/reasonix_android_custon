package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"reasonix/desktop/internal/workspacestate"
)

func TestPurgeCommandCrashRestart(t *testing.T) {
	for _, phase := range []string{"before-tombstone", "after-tombstone", "after-file-cleanup", "after-content-removed", "after-purge-committed", "before-command-result", "after-command-result"} {
		t.Run(phase, func(t *testing.T) {
			a, ref := lifecycleFixture(t)
			if err := a.ArchiveCanonicalSession(ref); err != nil {
				t.Fatal(err)
			}
			req := lifecycleRequest(t, a, ref, "crash-purge", "purge")
			body, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			root, registry := a.desktopSessions.root, a.workspaceRegistry().Path()
			a.closeSessionServices()
			run := func(point string, crash bool) {
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPurgeCommandCrashHelper$")
				cmd.Env = append(os.Environ(), "REASONIX_PURGE_APP_ROOT="+root, "REASONIX_PURGE_APP_REGISTRY="+registry, "REASONIX_PURGE_APP_REQUEST="+string(body), "REASONIX_PURGE_APP_POINT="+point)
				output, err := cmd.CombinedOutput()
				var exit *exec.ExitError
				if crash {
					if !errors.As(err, &exit) || exit.ExitCode() != 23 {
						t.Fatalf("checkpoint not reached: %v %s", err, output)
					}
				} else if err != nil {
					t.Fatalf("restart: %v %s", err, output)
				}
			}
			run(phase, true)
			store := workspacestate.NewStore(registry)
			state, err := store.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if phase == "before-tombstone" {
				if state.SessionStates[ref.SessionID].Lifecycle != workspacestate.Archived {
					t.Fatal("precommit lifecycle changed")
				}
				if _, err := os.Stat(filepath.Join(root, ref.SessionID)); err != nil {
					t.Fatalf("precommit content lost: %v", err)
				}
			} else {
				if state.SessionStates[ref.SessionID].Lifecycle != workspacestate.Deleted {
					t.Fatal("tombstone lost")
				}
				if err := store.RestoreSession(t.Context(), ref.SessionID); !errors.Is(err, workspacestate.ErrMutationConflict) {
					t.Fatalf("deleted session restored: %v", err)
				}
			}
			run("", false)
			first, err := os.ReadFile(registry)
			if err != nil {
				t.Fatal(err)
			}
			run("", false)
			second, err := os.ReadFile(registry)
			if err != nil || string(first) != string(second) {
				t.Fatalf("repeated replay rewrote registry: %v", err)
			}
			state, err = store.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if workspacestate.ClassifyPurge(state, ref.SessionID) != workspacestate.PurgeCommitted || state.PendingOperations["command-"+req.OperationID].Phase != "committed" {
				t.Fatal("incomplete receipts")
			}
			if _, err := os.Stat(filepath.Join(root, ref.SessionID)); !os.IsNotExist(err) {
				t.Fatalf("body survived deletion: %v", err)
			}
		})
	}
}

func TestPurgeCommandCrashHelper(t *testing.T) {
	root := os.Getenv("REASONIX_PURGE_APP_ROOT")
	if root == "" {
		return
	}
	a := NewApp()
	a.ctx = t.Context()
	a.desktopSessions.root = root
	a.desktopSessions.workspaceState = workspacestate.NewStore(os.Getenv("REASONIX_PURGE_APP_REGISTRY"))
	installNoopRuntimeEvents(a)
	defer a.closeSessionServices()
	var req SessionLifecycleRequest
	if err := json.Unmarshal([]byte(os.Getenv("REASONIX_PURGE_APP_REQUEST")), &req); err != nil {
		t.Fatal(err)
	}
	point := os.Getenv("REASONIX_PURGE_APP_POINT")
	if point != "" {
		a.lifecycleCheckpointHook = func(phase string) {
			if phase == point {
				os.Exit(23)
			}
		}
	} else {
		if err := a.recoverDesktopSessionOperations(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	result, err := a.ApplySessionLifecycle(req)
	if err != nil || !result.Committed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

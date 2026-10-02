package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func TestAttachSecondForkOfSameSourceDoesNotConflict(t *testing.T) {
	app := NewApp()
	app.ctx = t.Context()
	pinDesktopSessionRoot(t, app)
	installNoopRuntimeEvents(app, nil)
	root := globalWorkspaceRoot()
	if err := os.MkdirAll(desktopSessionDir(root), 0755); err != nil {
		t.Fatal(err)
	}
	service := app.desktopSessionService("")
	parent, err := service.Create(t.Context(), session.CreateOptions{SessionID: "fork-parent", CWD: root, Origin: session.SessionOriginNew})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"message": provider.Message{ID: "m1", Role: provider.RoleUser, Content: "hi"}})
	if _, err := parent.Session().Append(t.Context(), session.Batch{OperationID: "op1", TurnID: "turn-1", Events: []session.Event{
		{Kind: "turn/start"}, {Kind: "message/complete", Payload: payload}, {Kind: "turn/end", Payload: json.RawMessage(`{"status":"completed"}`)},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.attachDesktopSession(t.Context(), "global", "", parent.Ref()); err != nil {
		t.Fatal(err)
	}
	var sourceKey string
	for _, id := range []string{"fork-one", "fork-two"} {
		child, err := service.Fork(t.Context(), parent.Ref(), "turn-1", id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := app.attachDesktopSession(t.Context(), "global", "", child.Ref()); err != nil {
			t.Fatalf("attach %s: %v", id, err)
		}
		state, err := app.workspaceRegistry().Load(t.Context())
		if err != nil {
			t.Fatalf("registry load after %s: %v", id, err)
		}
		if id == "fork-one" {
			for key, m := range state.SourceMappings {
				if m.SessionID == "fork-one" {
					sourceKey = key
				}
			}
			if sourceKey == "" {
				t.Fatal("fork-one registered no source mapping")
			}
		}
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := state.SourceMappings[sourceKey].SessionID; got != "fork-one" {
		t.Fatalf("source mapping owner = %q, want fork-one", got)
	}
	for key, m := range state.SourceMappings {
		if m.SessionID == "fork-two" {
			t.Fatalf("fork-two must hold no mapping, found %s", key)
		}
	}
}

func TestForkSourceOwnedElsewhereOnlySkipsForks(t *testing.T) {
	app := NewApp()
	app.ctx = t.Context()
	pinDesktopSessionRoot(t, app)
	installNoopRuntimeEvents(app, nil)
	root := globalWorkspaceRoot()
	if err := os.MkdirAll(desktopSessionDir(root), 0755); err != nil {
		t.Fatal(err)
	}
	service := app.desktopSessionService("")
	var refs []session.SessionRef
	var workspaceID string
	for _, c := range []struct {
		id     string
		origin session.SessionOrigin
	}{{"owner", session.SessionOriginNew}, {"plain", session.SessionOriginNew}, {"forked", session.SessionOriginFork}} {
		h, err := service.Create(t.Context(), session.CreateOptions{SessionID: c.id, CWD: root, Origin: c.origin})
		if err != nil {
			t.Fatal(err)
		}
		id, err := app.attachDesktopSession(t.Context(), "global", "", h.Ref())
		if err != nil {
			t.Fatal(err)
		}
		workspaceID = id
		refs = append(refs, h.Ref())
	}
	path := filepath.Join(t.TempDir(), "legacy.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := app.recordDesktopSource(t.Context(), path, "legacy", "fp", "owner", workspaceID); err != nil {
		t.Fatal(err)
	}
	conflict := app.recordDesktopSource(t.Context(), path, "legacy", "fp", "plain", workspaceID)
	if !errors.Is(conflict, workspacestate.ErrMutationConflict) {
		t.Fatalf("setup: want conflict, got %v", conflict)
	}
	if app.forkSourceOwnedElsewhere(t.Context(), conflict, path, refs[1]) {
		t.Fatal("a non-fork session mapped elsewhere must surface the conflict")
	}
	if !app.forkSourceOwnedElsewhere(t.Context(), conflict, path, refs[2]) {
		t.Fatal("a fork whose source is owned by another session must be skipped")
	}
	if app.forkSourceOwnedElsewhere(t.Context(), errors.New("other"), path, refs[2]) {
		t.Fatal("only ErrMutationConflict may be skipped")
	}
}

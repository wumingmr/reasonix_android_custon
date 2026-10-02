package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func reopenLifecycleFixture(t *testing.T, a *App, ref session.SessionRef) (*session.Runtime, func()) {
	t.Helper()
	service := a.desktopSessionService("")
	binding, err := service.Open(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return binding.Runtime(), func() {
		if err := binding.Release(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := service.Close(context.Background(), ref); err != nil {
			t.Fatal(err)
		}
	}
}

func requireArchived(t *testing.T, a *App, ref session.SessionRef) {
	t.Helper()
	archived, err := a.ArchiveSessionTarget(SessionSelector{Ref: &ref})
	if err != nil || !archived.Committed {
		t.Fatalf("ArchiveSessionTarget = %+v, %v", archived, err)
	}
	state, err := a.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := state.SessionStates[ref.SessionID].Lifecycle; got != workspacestate.Archived {
		t.Fatalf("lifecycle = %q, want archived", got)
	}
}

// The session reopens with its durable history externalized, so the only
// record of the fixture's first message id is on disk.
func TestArchiveAfterACompletionReusesADurableMessageID(t *testing.T) {
	a, ref := lifecycleFixture(t)
	runtime, release := reopenLifecycleFixture(t, a, ref)
	payload, err := json.Marshal(map[string]any{"message": provider.Message{ID: "user", Role: provider.RoleUser, Origin: provider.MessageOriginHost, Content: "plan approved"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Session().AppendBatch(t.Context(), "reused-id", []session.Event{{Kind: "message/complete", Payload: payload}})
	if !errors.Is(err, session.ErrDuplicateMessageID) {
		t.Errorf("completion reusing a durable id: %v, want ErrDuplicateMessageID", err)
	}
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	release()
	requireArchived(t, a, ref)
}

// Archive is the user's way out of a session that no longer opens, so a
// damaged store must not block it.
func TestArchiveSessionWhoseStoreIsDamaged(t *testing.T) {
	a, ref := lifecycleFixture(t)
	runtime, release := reopenLifecycleFixture(t, a, ref)
	appendSessionTestMessage(t, runtime, "large", provider.Message{ID: "large", Role: provider.RoleAssistant, Content: strings.Repeat("x", 128<<10)})
	release()
	content := filepath.Join(a.desktopSessions.root, ".content-v1")
	if _, err := os.Stat(content); err != nil {
		t.Fatalf("content store: %v", err)
	}
	if err := os.RemoveAll(content); err != nil {
		t.Fatal(err)
	}
	if _, err := a.desktopSessionService("").Query().Snapshot(t.Context(), ref); !errors.Is(err, session.ErrDamagedStore) {
		t.Fatalf("snapshot of the damaged store: %v, want ErrDamagedStore", err)
	}
	requireArchived(t, a, ref)
}

func TestDamagedStoreFailureCarriesItsOwnCode(t *testing.T) {
	err := sessionOperationErrorForTarget(fmt.Errorf("%w: invalid message/complete payload at 7", session.ErrDamagedStore), "target", "op")
	var operationErr *SessionOperationError
	if !errors.As(err, &operationErr) || operationErr.Code != sessionOperationDamaged || operationErr.Retryable {
		t.Fatalf("classified as %#v, want %q", err, sessionOperationDamaged)
	}
	if strings.Contains(operationErr.Message, "message/complete") {
		t.Fatalf("user-visible message leaks the store detail: %q", operationErr.Message)
	}
}

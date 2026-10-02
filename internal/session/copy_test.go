package session

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"reasonix/internal/provider"
)

func TestCopySessionCopiesFullHistoryAndIsIdempotentPerOperation(t *testing.T) {
	service, err := NewService("local", NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions-v4")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "copy-source", CWD: "/workspace", Origin: SessionOriginNew})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"message": provider.Message{
		ID: "copy-message", Role: provider.RoleUser, Content: "complete copy history",
	}})
	if _, err := runtime.Session().AppendBatch(t.Context(), "copy-source-message", []Event{{Kind: "message/complete", Payload: payload}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	request := CopyRequest{Source: runtime.Ref(), OperationID: "copy-operation", CWD: "/workspace"}
	first, err := service.CopySession(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CopySession(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Child != second.Child || first.Child.SessionID == runtime.Ref().SessionID {
		t.Fatalf("copy identities = first:%+v second:%+v source:%+v", first.Child, second.Child, runtime.Ref())
	}
	history, err := service.Query().History(t.Context(), first.Child)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Content != "complete copy history" {
		t.Fatalf("copied history = %+v", history)
	}
	infos, err := service.Query().List(t.Context(), "", 10)
	if err != nil {
		t.Fatal(err)
	}
	var copied SessionInfo
	for _, info := range infos.Sessions {
		if info.SessionID == first.Child.SessionID {
			copied = info
			break
		}
	}
	if copied.ParentSessionID != runtime.Ref().SessionID || copied.Origin != SessionOriginCanonicalImport {
		t.Fatalf("copy header = %+v", copied)
	}
}

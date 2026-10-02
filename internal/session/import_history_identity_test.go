package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImportWithoutRecoveryIdentityPublishesReadableHistory(t *testing.T) {
	source, _, runtime := newSourceService(t, "old-archive")
	appendCompletedTurn(t, runtime, "turn", "historical-answer")
	bundle := filepath.Join(t.TempDir(), "bundle")
	if err := source.Export(t.Context(), runtime.Ref(), bundle); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(bundle, storageIdentityName)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"old-archive", "remapped-archive"} {
		t.Run(id, func(t *testing.T) {
			target, err := NewService("desktop", NewFilesystemPersistence(t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = target.Shutdown(t.Context()) })
			ref, err := target.ImportWithHeader(t.Context(), bundle, CreateOptions{SessionID: id, CWD: filepath.Clean("/workspace"), Origin: SessionOriginCanonicalImport})
			if err != nil {
				t.Fatal(err)
			}
			shape, err := target.Query().HistoryShape(t.Context(), ref)
			if err != nil || len(shape.Positions) != 1 {
				t.Fatalf("imported history unavailable before runtime: %+v %v", shape, err)
			}
			page := windowReady(t, target.Query(), ref, HistoryWindowRequest{Anchor: "newest", Limit: 10})
			if len(page.Messages) != 1 || page.Messages[0].MessageID != "historical-answer" {
				t.Fatalf("imported message changed: %+v", page)
			}
			if _, live := target.Runtime(ref); live {
				t.Fatal("history read started a runtime")
			}
			if _, err := os.Stat(filepath.Join(bundle, storageIdentityName)); !os.IsNotExist(err) {
				t.Fatalf("import modified the old archive: %v", err)
			}
		})
	}
}

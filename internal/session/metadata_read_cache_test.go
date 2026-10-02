package session

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestMetadataReadCacheObservesExternalHeaderReplacementAndCorruption(t *testing.T) {
	p := NewFilesystemPersistence(t.TempDir())
	w, err := p.Create(CreateOptions{SessionID: "cached", CWD: t.TempDir(), Origin: SessionOriginNew})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	first, err := p.Stat(t.Context(), "cached")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Stat(t.Context(), "cached"); err != nil {
		t.Fatal(err)
	}
	if len(p.metadataReads.entries) != 1 {
		t.Fatal("metadata read was not cached")
	}
	for _, invalid := range []string{"../cached", `..\cached`, "/cached", "cached/../cached", "."} {
		if _, err := p.Stat(t.Context(), invalid); err == nil {
			t.Fatalf("metadata lookup admitted path-shaped identity %q", invalid)
		}
	}
	path := filepath.Join(p.Root, "cached", sessionHeaderName)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body = bytes.Replace(body, []byte(`"origin":"new"`), []byte(`"origin":"fork"`), 1)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	second, err := p.Stat(t.Context(), "cached")
	if err != nil || first.Origin != SessionOriginNew || second.Origin != SessionOriginFork {
		t.Fatalf("external header stayed cached: %+v %v", second, err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Stat(t.Context(), "cached"); err == nil {
		t.Fatal("cached metadata hid header corruption")
	}
	if err := os.RemoveAll(filepath.Join(p.Root, "cached")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Stat(t.Context(), "cached"); err == nil {
		t.Fatal("cached session survived removal")
	}
}

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestHistoricalSidecarPreservesUnknownFieldsAndIndependentWriters(t *testing.T) {
	isolateDesktopUserDirs(t)
	path := historicalImportQueuePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"version":1,"current":"active","queue":["next"],"future":{"keep":true},"presentations":{"a":{"title":"old","futureBadge":"keep"}}}`)
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}
	a, b := newHistoricalLifecycleApp(t), newHistoricalLifecycleApp(t)
	for _, app := range []*App{a, b} {
		if _, err := app.ListHistoricalSessions(); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.saveHistoricalSourcePresentation("a", func(p *historicalSourcePresentation) { p.Title = "new" }); err != nil {
		t.Fatal(err)
	}
	if err := b.saveHistoricalSourcePresentation("a", func(p *historicalSourcePresentation) { pin := true; p.Pinned = &pin }); err != nil {
		t.Fatal(err)
	}
	saved, err := readHistoricalSidecar()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Current != "active" || len(saved.Queue) != 1 || saved.Queue[0] != "next" || saved.Presentations["a"].Title != "new" || saved.Presentations["a"].Pinned == nil || !*saved.Presentations["a"].Pinned {
		t.Fatalf("independent fields lost: %+v", saved)
	}
	a.historicalImports.mu.Lock()
	err = a.historicalImports.saveQueueLocked()
	a.historicalImports.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	saved, err = readHistoricalSidecar()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Presentations["a"].Title != "new" || saved.Presentations["a"].Pinned == nil {
		t.Fatalf("queue overwrote current presentations: %+v", saved)
	}
	data, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["future"]) != `{"keep":true}` || string(saved.Presentations["a"].extra["futureBadge"]) != `"keep"` {
		t.Fatalf("unknown fields lost: %s", data)
	}
	b.historicalImports.mu.Lock()
	err = b.historicalImports.saveQueueLocked()
	b.historicalImports.mu.Unlock()
	if err == nil {
		t.Fatal("stale batch revision overwrote a newer selection")
	}
}

func TestHistoricalQueueRejectsSecondProcessOwner(t *testing.T) {
	isolateDesktopUserDirs(t)
	a, b := newHistoricalLifecycleApp(t), newHistoricalLifecycleApp(t)
	a.historicalImports.mu.Lock()
	err := a.historicalImports.claimQueueLocked()
	a.historicalImports.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a.historicalImports.mu.Lock()
		a.historicalImports.releaseQueueLocked()
		a.historicalImports.mu.Unlock()
	})
	if _, err := b.StartHistoricalImport(nil); err == nil {
		t.Fatal("second owner started a competing batch")
	}
	if _, err := b.ControlHistoricalImport("cancel"); err == nil {
		t.Fatal("second owner cancelled another process batch")
	}
	if err := b.saveHistoricalSourcePresentation("a", func(p *historicalSourcePresentation) { p.Title = "still writable" }); err != nil {
		t.Fatal(err)
	}
}

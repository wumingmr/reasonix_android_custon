package sessioncatalog

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestDiscoveryDiagnosticsIdentifyPathOverflowCaller(t *testing.T) {
	var events []DiscoveryEvent
	c := &Catalog{
		opts:   Options{OnDiscovery: func(event DiscoveryEvent) { events = append(events, event) }},
		pathCh: make(chan sessionPathRequest), reconcileCh: make(chan DirectoryTarget, 1),
		reconcileDirty: map[string]DirectoryTarget{}, stop: make(chan struct{}),
	}
	root := t.TempDir()
	if c.RequestIndexSession(DirectoryTarget{Path: root}, filepath.Join(root, "old.jsonl")) {
		t.Fatal("unbuffered path queue unexpectedly accepted a request")
	}
	if len(events) != 1 || !strings.HasSuffix(events[0].Origin, ".TestDiscoveryDiagnosticsIdentifyPathOverflowCaller") {
		t.Fatalf("overflow diagnostic hid the request owner: %+v", events)
	}
}

func TestDiscoveryDiagnosticsCorrelateRequestAndDispatch(t *testing.T) {
	root := t.TempDir()
	var mu sync.Mutex
	var events []DiscoveryEvent
	c, err := Open(t.Context(), Options{InMemory: true, MetadataOnly: true, StartPaused: true,
		OnDiscovery: func(event DiscoveryEvent) {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	target := DirectoryTarget{Path: root, Scope: "global"}
	done, ok := c.ScheduleReconcile(target)
	if !ok {
		t.Fatal("request rejected")
	}
	if _, ok := c.ScheduleReconcile(target); !ok {
		t.Fatal("coalesced request rejected")
	}
	c.ResumeDiscovery()
	// The directory is empty, so the first iterator completes quickly. The
	// completion signal is the deterministic barrier for the event sequence.
	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal("discovery did not settle")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) < 3 {
		t.Fatalf("events=%+v", events)
	}
	rootID := events[0].Root
	started, completed := 0, 0
	for _, event := range events {
		if event.Root != rootID || event.Sequence == 0 {
			t.Fatalf("unrelated discovery identity: %+v", events)
		}
		switch event.Phase {
		case "started":
			started++
		case "completed":
			completed++
		}
	}
	if started != 1 || completed != 1 {
		t.Fatalf("pre-dispatch requests must share one scan: started=%d completed=%d events=%+v", started, completed, events)
	}
}

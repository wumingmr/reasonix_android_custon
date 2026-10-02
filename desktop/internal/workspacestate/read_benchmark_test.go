package workspacestate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A settled registry can have much larger completed journals than display data.
func BenchmarkRegistryRead(b *testing.B) {
	state := newState()
	for i := range 150 {
		id := fmt.Sprintf("session-%d", i)
		state.Presentation[id] = Presentation{Title: id}
		state.PendingOperations[id] = Operation{ID: id, Lifecycle: "active", Kind: "import", Phase: "committed", Request: json.RawMessage(`{"payload":"` + strings.Repeat("x", 5000) + `"}`)}
	}
	body, err := json.Marshal(state)
	if err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(b.TempDir(), "registry.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		b.Fatal(err)
	}
	for _, mode := range []string{"cold", "warm-full", "warm-projection"} {
		b.Run(mode, func(b *testing.B) {
			store := NewStore(path)
			if _, err := store.Load(b.Context()); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if mode == "cold" {
					store = NewStore(path)
				}
				var err error
				if mode == "warm-projection" {
					_, err = store.LoadProjection(b.Context())
				} else {
					_, err = store.Load(b.Context())
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

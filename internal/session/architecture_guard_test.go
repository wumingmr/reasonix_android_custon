package session

import (
	"os"
	"strings"
	"testing"
)

// The production open and derived-query paths must stay independent of the
// explicit full-history compatibility boundary. Migration, export and
// diagnostics use StreamSession instead.
func TestFastReadPathsDoNotMaterializeFullHistory(t *testing.T) {
	for _, path := range []string{"session_reader.go", "history_index.go", "search_index.go"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		source := string(data)
		for _, forbidden := range []string{".Snapshot()", ".History()", "materializeSnapshotMessages("} {
			if strings.Contains(source, forbidden) {
				t.Fatalf("%s reintroduced full-history call %q", path, forbidden)
			}
		}
	}
}

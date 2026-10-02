package serve

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reasonix/internal/session"
	"testing"
)

func TestHistoryOutlineHTTPIdentityAndFixedWindow(t *testing.T) {
	server, _ := newWindowTestServer(t)
	var window session.HistoryWindowPage
	getWindow(t, server, "anchor=newest&limit=1", &window)
	response, err := http.Get(server.URL + "/session-history/outline?sessionId=canonical&startTurn=1&limit=2")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var page session.HistoryOutlinePage
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil || response.StatusCode != http.StatusOK || page.Status != "ready" || page.TotalTurns != 8 || len(page.Entries) != 2 || page.Entries[0].MessageID != "m1" {
		t.Fatalf("outline: %+v %v", page, err)
	}
	getWindow(t, server, fmt.Sprintf("anchor=message&messageId=m1&direction=newer&limit=1&generation=%s&snapshotSequence=%d", page.Generation, page.SnapshotSequence), &window)
	if window.Status != "ready" || window.SnapshotSequence != page.SnapshotSequence || window.Messages[0].MessageID != "m1" {
		t.Fatalf("window: %+v", window)
	}
	getWindow(t, server, "anchor=message&messageId=m1&generation=wrong", &window)
	if window.Status != "stale_cursor" {
		t.Fatalf("lost generation: %+v", window)
	}
	for _, query := range []string{"sessionId=other", "sessionId=canonical&limit=bad", "sessionId=canonical&snapshotSequence=-1"} {
		response, err := http.Get(server.URL + "/session-history/outline?" + query)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode == http.StatusOK {
			t.Fatalf("accepted invalid request: %s", query)
		}
	}
}

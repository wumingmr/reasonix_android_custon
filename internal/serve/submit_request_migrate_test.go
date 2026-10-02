package serve

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// An HTTP client may start a migration but not choose the directory it reads:
// that is a host path only a local frontend names.
func TestSubmitRefusesAnExplicitMigrationSource(t *testing.T) {
	for input, allowed := range map[string]bool{
		`/migrate --from "C:\Users\someone\Old"`: false,
		"/migration /etc":                        false,
		"/migrate":                               true,
		"please /migrate --from x":               true,
	} {
		body, err := json.Marshal(map[string]string{"input": input})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/submit", bytes.NewReader(body))
		_, _, ok := decodeSubmitRequest(w, r)
		if ok != allowed {
			t.Fatalf("%q: accepted=%v, want %v (status %d)", input, ok, allowed, w.Code)
		}
		if !allowed && w.Code != http.StatusForbidden {
			t.Fatalf("%q: status %d, want 403", input, w.Code)
		}
	}
}

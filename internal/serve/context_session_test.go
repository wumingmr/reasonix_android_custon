package serve

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestContextReadRejectsStaleSessionIdentity(t *testing.T) {
	srv, _, _, current := newExclusiveSessionServe(t)
	server := httptest.NewServer(srv.Handler())
	defer server.Close()
	for _, test := range []struct {
		id   string
		want int
	}{
		{id: current.SessionID, want: http.StatusOK},
		{id: "other-session", want: http.StatusConflict},
	} {
		req, err := http.NewRequest(http.MethodGet, server.URL+"/context", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(expectedSessionIDHeader, test.id)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != test.want {
			t.Fatalf("session %q: status = %d, want %d", test.id, resp.StatusCode, test.want)
		}
	}
}

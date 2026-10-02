package serve

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGzipMiddleware(t *testing.T) {
	big := []byte(`{"data":"` + strings.Repeat("x", 8192) + `"}`)
	handler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/events" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {}\n\n"))
			return
		}
		w.Header().Set("ETag", `"abc"`)
		if r.Header.Get("If-None-Match") == `"abc"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(big)
	}))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/history", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := resp.Header.Get("ETag"); got != `"abc"` {
		t.Fatalf("ETag = %q, want preserved", got)
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plain, big) {
		t.Fatal("decompressed body mismatch")
	}

	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/history", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("If-None-Match", `"abc"`)
	resp304, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp304.Body.Close()
	if resp304.StatusCode != http.StatusNotModified || resp304.Header.Get("Content-Encoding") != "" {
		t.Fatalf("304 response = status %d encoding %q", resp304.StatusCode, resp304.Header.Get("Content-Encoding"))
	}

	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/events", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	events, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer events.Body.Close()
	if events.Header.Get("Content-Encoding") != "" {
		t.Fatal("SSE must bypass gzip")
	}
}

func TestGzipMiddlewareKeepsSmallResponsesPlain(t *testing.T) {
	h := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("small"))
	}))
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if got := rr.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("small response encoding = %q", got)
	}
	if rr.Body.String() != "small" {
		t.Fatalf("small body = %q", rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("small response Content-Type = %q", got)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("small response X-Content-Type-Options = %q", got)
	}
}

func TestGzipMiddlewareDoesNotRenderUntypedReflectedHTML(t *testing.T) {
	h := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.URL.Query().Get("value")))
	}))
	req := httptest.NewRequest(http.MethodGet, "/status?value=%3Cscript%3Ealert(1)%3C/script%3E", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if got := rr.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("reflected response Content-Type = %q", got)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("reflected response X-Content-Type-Options = %q", got)
	}
}

func TestGzipMiddlewareHonorsDisabledEncoding(t *testing.T) {
	h := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", gzipThreshold+1)))
	}))
	for _, encoding := range []string{"gzip;q=0", "br, *;q=1, gzip;q=0"} {
		req := httptest.NewRequest(http.MethodGet, "/history", nil)
		req.Header.Set("Accept-Encoding", encoding)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if got := rr.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("Accept-Encoding %q produced %q", encoding, got)
		}
	}
}

func TestGzipPlainWritesPreserveDeclaredMediaType(t *testing.T) {
	for _, mediaType := range []string{"", "application/json", "text/html; charset=utf-8", "application/octet-stream"} {
		t.Run(mediaType, func(t *testing.T) {
			rr := httptest.NewRecorder()
			if mediaType != "" {
				rr.Header().Set("Content-Type", mediaType)
			}
			writer := &gzipBufferedWriter{ResponseWriter: rr}
			writer.startPlain()
			payload := []byte("<script>alert(1)</script>")
			if _, err := writer.Write(payload); err != nil {
				t.Fatal(err)
			}
			response := rr.Result()
			defer response.Body.Close()
			wantType := mediaType
			if wantType == "" {
				wantType = "text/plain; charset=utf-8"
			}
			if got := response.Header.Get("Content-Type"); got != wantType {
				t.Fatalf("committed content type = %q, want %q", got, wantType)
			}
			if response.Header.Get("X-Content-Type-Options") != "nosniff" || !bytes.Equal(rr.Body.Bytes(), payload) {
				t.Fatal("plain write changed payload or omitted nosniff")
			}
		})
	}
}

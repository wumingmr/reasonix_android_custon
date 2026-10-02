package persistentshell

import (
	"encoding/json"
	"reasonix/internal/shellrun"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCaptureProgressPreservesUTF8AcrossMarkerHold(t *testing.T) {
	var received strings.Builder
	w := shellrun.NewProgressWriter(func(s string) {
		if !utf8.ValidString(s) {
			t.Fatalf("invalid event: %x", []byte(s))
		}
		encoded, _ := json.Marshal(s)
		var decoded string
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		received.WriteString(decoded)
	})
	c := newCapture("START", "END:", w)
	want := "中" + strings.Repeat("a", markerOverlap-2)
	c.push("START\n" + want)
	c.push("END:0\n")
	w.Flush()
	if !c.done || c.body() != want || received.String() != want {
		t.Fatalf("stream=%q final=%q", received.String(), c.body())
	}
}

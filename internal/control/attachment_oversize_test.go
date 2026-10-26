package control

import (
	"encoding/base64"
	"errors"
	"runtime"
	"strings"
	"testing"
)

func TestBase64DecodedCeilingBoundsEveryValidPayload(t *testing.T) {
	for n := range 64 {
		enc := base64.StdEncoding.EncodedLen(n)
		if got := base64DecodedCeiling(enc); got < n-2 || got > n+2 {
			t.Fatalf("n=%d encoded=%d ceiling=%d", n, enc, got)
		}
	}
}

func TestOversizeDataURLIsRefusedBeforeDecoding(t *testing.T) {
	dir := t.TempDir()
	payload := strings.Repeat("A", base64.StdEncoding.EncodedLen(maxFileAttachmentBytes)+4096)
	url := "data:application/octet-stream;base64," + payload
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := SaveAttachmentDataURLInRoot(dir, "pack.mrpack", url)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrAttachmentTooLarge) {
		t.Fatalf("err = %v, want ErrAttachmentTooLarge", err)
	}
	// A decode would allocate ~25 MB.
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 8<<20 {
		t.Fatalf("refusal allocated %d bytes", grew)
	}
}

func TestOversizeImageDataURLIsRefusedBeforeDecoding(t *testing.T) {
	payload := strings.Repeat("A", base64.StdEncoding.EncodedLen(maxImageAttachmentBytes)+4096)
	_, err := SaveImageDataURLInRoot(t.TempDir(), "data:image/png;base64,"+payload)
	if !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("err = %v, want ErrImageTooLarge", err)
	}
}

func TestAttachmentAtTheLimitStillSaves(t *testing.T) {
	raw := make([]byte, maxFileAttachmentBytes)
	raw[0] = 'P'
	url := "data:application/zip;base64," + base64.StdEncoding.EncodeToString(raw)
	if _, err := SaveAttachmentDataURLInRoot(t.TempDir(), "edge.zip", url); err != nil {
		t.Fatalf("limit-sized attachment refused: %v", err)
	}
}

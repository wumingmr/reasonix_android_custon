package session

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"reasonix/internal/sessioncontent"
)

func TestV4CodecExternalizesLargePayloadAndRoundTripsExactBytes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	content := sessioncontent.New(filepath.Join(dir, "content"))
	payload := append([]byte(`{"text":"`), bytes.Repeat([]byte("x"), v4InlinePayloadBytes+12345)...)
	payload = append(payload, []byte(`"}`)...)
	commit := v4TestCommit(payload)
	var log bytes.Buffer
	lengths, err := encodeV4Commits(context.Background(), &log, content, []Commit{commit})
	if err != nil {
		t.Fatalf("encodeV4Commits: %v", err)
	}
	if len(lengths) != 1 || lengths[0] != int64(log.Len()) {
		t.Fatalf("lengths = %v, log bytes = %d", lengths, log.Len())
	}
	for encoded := log.Bytes(); len(encoded) > 0; {
		if len(encoded) < v4FrameHeaderBytes {
			t.Fatalf("short physical frame header: %d", len(encoded))
		}
		compressed := int(binary.BigEndian.Uint32(encoded[4:8]))
		raw := int(binary.BigEndian.Uint32(encoded[8:12]))
		if compressed > v4MaxFrameBytes || raw > v4MaxFrameBytes {
			t.Fatalf("frame exceeds reader budget: compressed=%d raw=%d", compressed, raw)
		}
		encoded = encoded[v4FrameHeaderBytes+compressed:]
	}

	path := filepath.Join(dir, "events.v4")
	if err := os.WriteFile(path, log.Bytes(), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var got []Commit
	err = scanV4CommitFile(context.Background(), f, 0, 1, content, nil, func(_ int64, commit Commit) bool {
		got = append(got, commit)
		return true
	})
	if err != nil {
		t.Fatalf("scanV4CommitFile: %v", err)
	}
	if len(got) != 1 || len(got[0].Events) != 1 || !bytes.Equal(got[0].Events[0].Payload, payload) {
		t.Fatalf("round trip mismatch: commits=%d payload=%d", len(got), payloadLen(got))
	}

	entries, err := filepath.Glob(filepath.Join(content.Root(), "objects", "*", "*", "*"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("external objects = %v, err=%v", entries, err)
	}
}

func TestV4ReferenceScanDoesNotMaterializeLargePayload(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	content := sessioncontent.New(filepath.Join(dir, "content"))
	payload := bytes.Repeat([]byte("p"), 2*v4InlinePayloadBytes)
	commit := v4TestCommit(payload)
	var log bytes.Buffer
	if _, err := encodeV4Commits(t.Context(), &log, content, []Commit{commit}); err != nil {
		t.Fatal(err)
	}
	file := writeAndOpenV4TestLog(t, dir, "lazy.v4", log.Bytes())
	defer file.Close()
	if err := scanV4CommitFileRefs(t.Context(), file, 0, 1, content, nil, func(_ int64, got Commit) bool {
		if len(got.Events) != 1 || len(got.Events[0].Payload) != 0 || got.Events[0].PayloadRef == nil || got.Events[0].PayloadRef.Bytes != int64(len(payload)) {
			t.Fatalf("lazy event = %#v", got.Events)
		}
		return true
	}); err != nil {
		t.Fatal(err)
	}
}

func TestV4ReaderDoesNotPreallocateUntrustedEventCount(t *testing.T) {
	t.Parallel()
	record := v4Record{
		SchemaVersion: V4SchemaVersion, Codec: V4Codec, RecordType: "batch/begin",
		CommitID: "c", OperationID: "o", OperationHash: "h", FirstSequence: 1,
		EventCount: int(^uint(0) >> 2), WriterGeneration: 1,
	}
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	var log bytes.Buffer
	if err := writeV4Record(t.Context(), &log, encoder, record); err != nil {
		t.Fatal(err)
	}
	file := writeAndOpenV4TestLog(t, t.TempDir(), "count.v4", log.Bytes())
	defer file.Close()
	if err := scanV4CommitFileRefs(t.Context(), file, 0, 1, nil, nil, nil); err != nil {
		t.Fatalf("incomplete declared transaction should remain invisible: %v", err)
	}
}

func TestV4CodecHidesIncompleteBatchAndRejectsCorruption(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	content := sessioncontent.New(filepath.Join(dir, "content"))
	commit := v4TestCommit(json.RawMessage(`{"message":"durable only after end"}`))
	var log bytes.Buffer
	if _, err := encodeV4Commits(context.Background(), &log, content, []Commit{commit}); err != nil {
		t.Fatalf("encode: %v", err)
	}

	truncated := append([]byte(nil), log.Bytes()[:log.Len()-5]...)
	f1 := writeAndOpenV4TestLog(t, dir, "truncated.v4", truncated)
	visited := 0
	if err := scanV4CommitFile(context.Background(), f1, 0, 1, content, nil, func(_ int64, _ Commit) bool {
		visited++
		return true
	}); err != nil {
		t.Fatalf("incomplete tail should be ignored, got %v", err)
	}
	_ = f1.Close()
	if visited != 0 {
		t.Fatalf("incomplete transaction became visible: %d commits", visited)
	}

	corrupt := append([]byte(nil), log.Bytes()...)
	if len(corrupt) < v4FrameHeaderBytes+8 {
		t.Fatal("encoded test log unexpectedly small")
	}
	corrupt[v4FrameHeaderBytes+4] ^= 0x7f
	f2 := writeAndOpenV4TestLog(t, dir, "corrupt.v4", corrupt)
	defer f2.Close()
	if err := scanV4CommitFile(context.Background(), f2, 0, 1, content, nil, nil); err == nil {
		t.Fatal("corrupt complete frame was accepted")
	}
}

func v4TestCommit(payload json.RawMessage) Commit {
	return Commit{
		SchemaVersion:    4,
		Codec:            V4Codec,
		RecordType:       "commit",
		ID:               "commit-1",
		OperationID:      "operation-1",
		OperationHash:    "hash-1",
		FirstSequence:    1,
		EventCount:       1,
		TurnID:           "turn-1",
		WriterGeneration: 1,
		CreatedAt:        time.Unix(100, 0).UTC(),
		Events:           []Event{{ID: "event-1", Sequence: 1, Kind: "diagnostic", Payload: payload}},
	}
}

func payloadLen(commits []Commit) int {
	if len(commits) == 0 || len(commits[0].Events) == 0 {
		return 0
	}
	return len(commits[0].Events[0].Payload)
}

func writeAndOpenV4TestLog(t *testing.T, dir, name string, data []byte) *os.File {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

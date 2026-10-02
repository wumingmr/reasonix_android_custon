package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestPagedReadBuildsAndUsesSparseCommitIndex(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "indexed")
	store, err := Open(dir, "indexed")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 600 {
		payload, _ := json.Marshal(map[string]int{"number": i})
		if _, err := store.Append(t.Context(), Batch{
			OperationID: "diagnostic-" + strconv.Itoa(i),
			Events:      []Event{{Kind: "diagnostic/test", Optional: true, Payload: payload}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	handle, err := openReadHandle(dir, "indexed")
	if err != nil {
		t.Fatal(err)
	}
	page, err := handle.Read(context.Background(), 520, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Commits) != 3 || page.Commits[0].FirstSequence != 521 || page.Next != 523 || !page.Truncated {
		t.Fatalf("page = %+v", page)
	}
	data, err := os.ReadFile(sparseIndexPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var index sparseIndex
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatal(err)
	}
	if len(index.Entries) != 3 || index.Entries[1].FirstSequence != 257 || index.Entries[2].FirstSequence != 513 {
		t.Fatalf("sparse entries = %+v", index.Entries)
	}
}

func TestCorruptSparseIndexIsRebuiltFromValidatedLog(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rebuild")
	store, err := Open(dir, "rebuild")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(t.Context(), Batch{OperationID: "one", Events: []Event{{Kind: "diagnostic/test", Optional: true}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sparseIndexPath(dir), []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	page, err := readCommitPage(context.Background(), dir, 0, 10)
	if err != nil || len(page.Commits) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	data, err := os.ReadFile(sparseIndexPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var rebuilt sparseIndex
	if err := json.Unmarshal(data, &rebuilt); err != nil || rebuilt.Codec != sparseIndexCodec || rebuilt.LastSequence != 1 {
		t.Fatalf("rebuilt=%+v err=%v", rebuilt, err)
	}
}

func TestReopenedWriterKeepsSparseIndexCurrentAcrossAppends(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "reopened")
	writer, err := Open(dir, "reopened")
	if err != nil {
		t.Fatal(err)
	}
	appendEvent := func(operation string) {
		t.Helper()
		if _, err := writer.Append(t.Context(), Batch{OperationID: operation, Events: []Event{{Kind: "diagnostic/test", Optional: true}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Flush(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent("initial")
	if err := writer.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	writer, err = Open(dir, "reopened")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close(t.Context())
	for i := range 2 {
		appendEvent("after-reopen-" + strconv.Itoa(i))
		before, err := os.Stat(sparseIndexPath(dir))
		if err != nil {
			t.Fatal(err)
		}
		page, err := writer.Read(t.Context(), 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Commits) != i+2 {
			t.Fatalf("read %d: got %d commits", i, len(page.Commits))
		}
		after, err := os.Stat(sparseIndexPath(dir))
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(before, after) {
			t.Fatalf("read %d rebuilt the sparse index after append", i)
		}
	}
}

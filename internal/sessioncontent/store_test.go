package sessioncontent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestStorePutOpenAndDeduplicate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := New(root)
	body := bytes.Repeat([]byte("reasonix-content\n"), 10000)
	sum := sha256.Sum256(body)
	wantDigest := hex.EncodeToString(sum[:])

	first, err := store.Put(context.Background(), bytes.NewReader(body), Metadata{
		MediaType: "text/plain; charset=utf-8",
		Name:      "../../not-a-storage-path.txt",
	})
	if err != nil {
		t.Fatalf("Put first: %v", err)
	}
	second, err := store.Put(context.Background(), bytes.NewReader(body), Metadata{
		MediaType: "application/octet-stream",
		Name:      "different-display-name.bin",
	})
	if err != nil {
		t.Fatalf("Put second: %v", err)
	}
	if first.Digest != wantDigest || second.Digest != wantDigest {
		t.Fatalf("digest = %q / %q, want %q", first.Digest, second.Digest, wantDigest)
	}
	if first.Bytes != int64(len(body)) || second.Bytes != int64(len(body)) {
		t.Fatalf("bytes = %d / %d, want %d", first.Bytes, second.Bytes, len(body))
	}
	if first.Name != "../../not-a-storage-path.txt" || second.Name != "different-display-name.bin" {
		t.Fatalf("display metadata was not preserved: %#v / %#v", first, second)
	}

	r, err := store.Open(context.Background(), first)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, readErr := io.ReadAll(r)
	closeErr := r.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read/close: %v / %v", readErr, closeErr)
	}
	if !bytes.Equal(got, body) {
		t.Fatal("opened bytes differ from input")
	}

	var objects int
	err = filepath.WalkDir(filepath.Join(root, "objects"), func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			objects++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk objects: %v", err)
	}
	if objects != 1 {
		t.Fatalf("physical objects = %d, want 1", objects)
	}
	if _, err := os.Stat(filepath.Join(root, "not-a-storage-path.txt")); !os.IsNotExist(err) {
		t.Fatalf("display name escaped content store: %v", err)
	}
}

func TestStoreReadRangeAndStat(t *testing.T) {
	t.Parallel()
	store := New(t.TempDir())
	body := []byte("0123456789")
	ref, err := store.Put(context.Background(), bytes.NewReader(body), Metadata{MediaType: "text/plain"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := store.ReadRange(context.Background(), ref, 3, 4)
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	if string(got) != "3456" {
		t.Fatalf("range = %q, want 3456", got)
	}
	stat, err := store.Stat(context.Background(), ref)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stat.Digest != ref.Digest || stat.Bytes != int64(len(body)) {
		t.Fatalf("Stat = %#v", stat)
	}
	if _, err := store.ReadRange(context.Background(), ref, -1, 1); err == nil {
		t.Fatal("negative range offset was accepted")
	}
	if _, err := store.ReadRange(context.Background(), ref, 9, 2); err == nil {
		t.Fatal("out-of-bounds range was accepted")
	}
}

func TestStoreRejectsUntrustedIntegrityIndexPath(t *testing.T) {
	t.Parallel()
	store := New(t.TempDir())
	ref := Ref{Digest: "../../outside", Bytes: 1, IndexDigest: strings.Repeat("0", sha256.Size*2), IntegrityBlock: IntegrityBlockBytes}
	if _, err := store.readIndex(t.Context(), ref); err == nil {
		t.Fatal("readIndex accepted an untrusted digest path")
	}
}

func TestStoreReadRangeVerifiesOnlyTouchedIntegrityBlocks(t *testing.T) {
	t.Parallel()
	store := New(t.TempDir())
	body := bytes.Repeat([]byte("a"), 3*IntegrityBlockBytes)
	ref, err := store.Put(t.Context(), bytes.NewReader(body), Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	object, err := os.OpenFile(store.objectPath(ref.Digest), os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := object.WriteAt([]byte("x"), 16); err != nil {
		_ = object.Close()
		t.Fatal(err)
	}
	if err := object.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := store.ReadRange(t.Context(), ref, 2*IntegrityBlockBytes, 32); err != nil || !bytes.Equal(got, body[2*IntegrityBlockBytes:2*IntegrityBlockBytes+32]) {
		t.Fatalf("untouched range = %d bytes, err=%v", len(got), err)
	}
	if _, err := store.ReadRange(t.Context(), ref, 0, 32); err == nil {
		t.Fatal("tampered range passed block verification")
	}
	if err := store.Verify(t.Context(), ref); err == nil {
		t.Fatal("full verification accepted tampered object")
	}
}

func TestStoreRejectsCancellationAndTampering(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := New(root)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Put(canceled, bytes.NewReader([]byte("never published")), Metadata{}); err == nil {
		t.Fatal("Put with canceled context succeeded")
	}

	ref, err := store.Put(context.Background(), bytes.NewReader([]byte("original")), Metadata{})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := os.WriteFile(store.objectPath(ref.Digest), []byte("tampered"), 0o600); err != nil {
		t.Fatalf("tamper object: %v", err)
	}
	if _, err := store.Open(context.Background(), ref); err == nil {
		t.Fatal("Open accepted a tampered object")
	}
	if _, err := store.Stat(context.Background(), ref); err == nil {
		t.Fatal("Stat accepted a tampered object")
	}
}

func TestStoreConcurrentPutSameContent(t *testing.T) {
	t.Parallel()
	store := New(t.TempDir())
	body := bytes.Repeat([]byte("same"), 250000)
	const workers = 8
	refs := make([]Ref, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			refs[i], errs[i] = store.Put(context.Background(), bytes.NewReader(body), Metadata{})
		})
	}
	wg.Wait()
	for i := range workers {
		if errs[i] != nil {
			t.Fatalf("Put[%d]: %v", i, errs[i])
		}
		if refs[i].Digest != refs[0].Digest {
			t.Fatalf("digest[%d] = %q, want %q", i, refs[i].Digest, refs[0].Digest)
		}
	}
	r, err := store.Open(context.Background(), refs[0])
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("published object invalid: err=%v bytes=%d", err, len(got))
	}
}

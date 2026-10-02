package session

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"reasonix/internal/identitylock"
	"reasonix/internal/provider"
)

func TestTryExportColdRetainsSourceAndDoesNotWaitForOwner(t *testing.T) {
	for _, owner := range []string{"same-process", "other-process"} {
		for _, lock := range []string{"writer", "ownership"} {
			t.Run(owner+"/"+lock, func(t *testing.T) {
				root := filepath.Join(t.TempDir(), "source")
				service, err := NewService("export-source", NewFilesystemPersistence(root))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
				runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "history"})
				if err != nil {
					t.Fatal(err)
				}
				content := strings.Repeat("retained historical content ", 20_000)
				payload, err := json.Marshal(map[string]any{"message": provider.Message{ID: "message", Role: provider.RoleUser, Content: content}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := runtime.Session().AppendBatch(t.Context(), "message", []Event{{Kind: "message/complete", Payload: payload}}); err != nil {
					t.Fatal(err)
				}
				ref := runtime.Ref()
				if err := service.Close(t.Context(), ref); err != nil {
					t.Fatal(err)
				}
				source := filepath.Join(root, ref.SessionID)
				original := map[string][]byte{}
				for _, name := range []string{"manifest.json", "events.frames"} {
					original[name], err = os.ReadFile(filepath.Join(source, name))
					if err != nil {
						t.Fatal(err)
					}
				}
				lockPath := filepath.Join(source, "writer.lock")
				if lock == "ownership" {
					lockPath = directoryOwnershipPath(source)
				}
				var release func()
				if owner == "other-process" {
					release = holdTryExportLockInChild(t, lockPath)
				} else {
					release, err = identitylock.Acquire(t.Context(), lockPath)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(release)
				}
				destination := filepath.Join(t.TempDir(), "bundle")
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				err = service.TryExportCold(ctx, ref, destination)
				cancel()
				if !errors.Is(err, identitylock.ErrHeld) {
					t.Fatalf("busy source must return ErrHeld, not wait for deadline: %v", err)
				}
				if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("busy source published an export: %v", err)
				}
				release()
				if err := service.TryExportCold(t.Context(), ref, destination); err != nil {
					t.Fatalf("export did not resume after source release: %v", err)
				}
				for name, before := range original {
					after, err := os.ReadFile(filepath.Join(source, name))
					if err != nil || !bytes.Equal(before, after) {
						t.Fatalf("source %s changed: %v", name, err)
					}
				}
				if _, err := Replay(destination, nil); err != nil {
					t.Fatalf("exported bundle is not readable: %v", err)
				}
				if err := os.RemoveAll(root); err != nil {
					t.Fatal(err)
				}
				if _, err := Replay(destination, nil); err != nil {
					t.Fatalf("export depends on original source: %v", err)
				}
				target, err := NewService("export-target", NewFilesystemPersistence(filepath.Join(t.TempDir(), "imported")))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = target.Shutdown(context.Background()) })
				imported, err := target.Import(t.Context(), destination)
				if err != nil {
					t.Fatal(err)
				}
				page := historyPageReady(t, target.Query(), imported, "", 10)
				if len(page.Messages) != 1 {
					t.Fatal("export did not preserve the historical message")
				}
				stored := page.Messages[0]
				body := []byte(stored.Inline)
				if stored.ContentRef != nil {
					body, err = target.Query().ReadContent(t.Context(), imported, *stored.ContentRef, 0, stored.ContentRef.Bytes)
					if err != nil {
						t.Fatal(err)
					}
				}
				var message provider.Message
				if err := json.Unmarshal(body, &message); err != nil {
					t.Fatal(err)
				}
				if message.Content != content {
					t.Fatal("export did not preserve the complete historical message")
				}
			})
		}
	}
}

// The child holds an actual operating-system lock, exercising LockFileEx on
// Windows rather than only identitylock's process-local coordination.
func TestTryExportColdLockHelper(t *testing.T) {
	path := os.Getenv("REASONIX_TEST_TRY_EXPORT_LOCK")
	if path == "" {
		return
	}
	release, err := identitylock.Acquire(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	fmt.Fprintln(os.Stdout, "LOCKED")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func holdTryExportLockInChild(t *testing.T, path string) func() {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTryExportColdLockHelper$")
	cmd.Env = append(os.Environ(), "REASONIX_TEST_TRY_EXPORT_LOCK="+path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	var waitErr error
	release := func() {
		once.Do(func() {
			_ = stdin.Close()
			waitErr = cmd.Wait()
		})
		if waitErr != nil {
			t.Errorf("source lock helper failed: %v: %s", waitErr, stderr.String())
		}
	}
	t.Cleanup(release)
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "LOCKED" {
		t.Fatalf("source lock helper did not become ready: %v", scanner.Err())
	}
	return release
}

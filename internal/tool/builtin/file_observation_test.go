package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/fileops"
	"reasonix/internal/tool"
)

func observedContext() context.Context {
	return fileops.WithStore(context.Background(), fileops.NewStore())
}

func TestWindowReadAuthorizesConsecutiveEdits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "中文.txt")
	if err := os.WriteFile(path, []byte("alpha\r\nbeta\r\ngamma\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := observedContext()
	r := readFile{workDir: dir}
	if _, _, err := r.ExecuteRead(ctx, json.RawMessage(`{"path":"中文.txt","limit":1}`)); err != nil {
		t.Fatal(err)
	}
	e := editFile{workDir: dir}
	if _, err := e.Execute(ctx, json.RawMessage(`{"path":"中文.txt","old_string":"beta","new_string":"BETA"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(ctx, json.RawMessage(`{"path":"中文.txt","old_string":"gamma","new_string":"GAMMA"}`)); err != nil {
		t.Fatalf("successful write did not refresh observation: %v", err)
	}
}

func TestUnobservedOverwriteRejectedButCreationAllowed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := observedContext()
	w := writeFile{workDir: dir}
	_, err := w.Execute(ctx, json.RawMessage(`{"path":"existing","content":"new"}`))
	var opErr *tool.OperationError
	if !errors.As(err, &opErr) || opErr.Diagnostic.Code != tool.FSNotObserved {
		t.Fatalf("unobserved overwrite error = %v", err)
	}
	if _, err := w.Execute(ctx, json.RawMessage(`{"path":"created","content":"new"}`)); err != nil {
		t.Fatalf("unobserved create failed: %v", err)
	}
}

func TestExternalChangeMakesObservationStale(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := observedContext()
	r := readFile{workDir: dir}
	if _, _, err := r.ExecuteRead(ctx, json.RawMessage(`{"path":"file","limit":1}`)); err != nil {
		t.Fatal(err)
	}
	stat, _ := os.Stat(path)
	if err := os.WriteFile(path, []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, stat.ModTime(), stat.ModTime()); err != nil {
		t.Fatal(err)
	}
	e := editFile{workDir: dir}
	_, err := e.Execute(ctx, json.RawMessage(`{"path":"file","old_string":"two","new_string":"THREE"}`))
	var opErr *tool.OperationError
	if !errors.As(err, &opErr) || opErr.Diagnostic.Code != tool.FSStaleVersion {
		t.Fatalf("stale edit error = %v", err)
	}
	if _, _, err := r.ExecuteRead(ctx, json.RawMessage(`{"path":"file","limit":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(ctx, json.RawMessage(`{"path":"file","old_string":"two","new_string":"THREE"}`)); err != nil {
		t.Fatalf("reread did not recover: %v", err)
	}
}

func TestLargeWindowReadDoesNotCaptureWholeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("a bounded local line\n", 20000)), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := observedContext()
	r := readFile{workDir: dir}
	output, env, err := r.ExecuteRead(ctx, json.RawMessage(`{"path":"large.txt","offset":100,"limit":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if env.Source.Snapshot == "" || len(output) > 300 {
		t.Fatalf("window/version result unexpected: output=%d snapshot=%q", len(output), env.Source.Snapshot)
	}
}

func TestConcurrentCreateNeverOverwritesWinner(t *testing.T) {
	dir := t.TempDir()
	ctxA, ctxB := observedContext(), observedContext()
	w := writeFile{workDir: dir}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, item := range []struct {
		ctx     context.Context
		content string
	}{{ctxA, "a"}, {ctxB, "b"}} {
		go func(ctx context.Context, content string) {
			<-start
			_, err := w.Execute(ctx, json.RawMessage(`{"path":"new","content":"`+content+`"}`))
			results <- err
		}(item.ctx, item.content)
	}
	close(start)
	err1, err2 := <-results, <-results
	if (err1 == nil) == (err2 == nil) {
		t.Fatalf("want one winner and one rejection: %v / %v", err1, err2)
	}
	got, err := os.ReadFile(filepath.Join(dir, "new"))
	if err != nil || (string(got) != "a" && string(got) != "b") {
		t.Fatalf("winner content = %q, %v", got, err)
	}
}

func TestConcurrentEditsFromSameVersionDoNotLoseUpdate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shared.txt")
	if err := os.WriteFile(path, []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctxA, ctxB := observedContext(), observedContext()
	r := readFile{workDir: dir}
	for _, ctx := range []context.Context{ctxA, ctxB} {
		if _, _, err := r.ExecuteRead(ctx, json.RawMessage(`{"path":"shared.txt","limit":1}`)); err != nil {
			t.Fatal(err)
		}
	}
	e := editFile{workDir: dir}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, item := range []struct {
		ctx context.Context
		to  string
	}{{ctxA, "agent-a"}, {ctxB, "agent-b"}} {
		go func(ctx context.Context, to string) {
			<-start
			_, err := e.Execute(ctx, json.RawMessage(`{"path":"shared.txt","old_string":"base","new_string":"`+to+`"}`))
			results <- err
		}(item.ctx, item.to)
	}
	close(start)
	err1, err2 := <-results, <-results
	if (err1 == nil) == (err2 == nil) {
		t.Fatalf("want one committed edit and one stale edit: %v / %v", err1, err2)
	}
	loser := err1
	if loser == nil {
		loser = err2
	}
	var opErr *tool.OperationError
	if !errors.As(loser, &opErr) || opErr.Diagnostic.Code != tool.FSStaleVersion {
		t.Fatalf("loser error = %v", loser)
	}
	got, err := os.ReadFile(path)
	if err != nil || (string(got) != "agent-a\n" && string(got) != "agent-b\n") {
		t.Fatalf("final content=%q err=%v", got, err)
	}
}

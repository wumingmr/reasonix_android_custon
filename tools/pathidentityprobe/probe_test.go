package main

import (
	"context"
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"reasonix/internal/pathidentity"
)

func TestInspectDoesNotReadContentOrCreateMissingTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.lock")
	const secret = "sentinel-credential-must-not-appear"
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	got := inspect(path)
	if !got.Lstat.OK || !got.Eval.OK || !got.Resolve.OK {
		t.Fatalf("ordinary file failed: %+v", got)
	}
	raw, err := json.Marshal(got)
	if err != nil || strings.Contains(string(raw), secret) {
		t.Fatalf("invalid or content-bearing report: %s, %v", raw, err)
	}
	after, err := os.Stat(path)
	if err != nil || !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatalf("file metadata changed: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != secret {
		t.Fatalf("file changed: %v", err)
	}
	missing := filepath.Join(dir, "absent", "child.lock")
	result := inspect(missing)
	if result.Lstat.OK || result.Eval.OK || !result.Resolve.OK {
		t.Fatalf("missing-tail behavior: %+v", result)
	}
	if _, err := os.Lstat(filepath.Dir(missing)); !os.IsNotExist(err) {
		t.Fatalf("probe created missing parent: %v", err)
	}
}

func TestFailureRetainsStagePathAndSystemCode(t *testing.T) {
	err := &pathidentity.Error{Stage: "physical", Path: "missing.lock", Kind: pathidentity.ErrorUnavailable,
		Err: &os.PathError{Op: "readlink", Path: "junction", Err: syscall.Errno(3)}}
	got := failureOutcome(err)
	if got.OK || len(got.Errors) != 3 || got.Errors[0].Stage != "physical" ||
		got.Errors[0].Path != "missing.lock" || got.Errors[1].Path != "junction" || got.Errors[2].Code != 3 {
		t.Fatalf("lost structured failure: %+v", got)
	}
}

func TestCollectionRedactsNestedErrorsAndDoesNotCreateLocks(t *testing.T) {
	home := filepath.Join(t.TempDir(), "probe-private-user")
	current := &user.User{HomeDir: home, Uid: "S-1-5-21-private-fixture", Username: "probe-private-user"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r := collect(ctx, current, nil)
	if r.TimedOut || len(r.Paths) == 0 {
		t.Fatalf("no collection: %+v", r)
	}
	r.Notes = append(r.Notes, current.Uid)
	r.Paths[0].Resolve = failureOutcome(&pathidentity.Error{Stage: "physical", Path: home,
		Err: &os.PathError{Op: "open", Path: filepath.Join(home, "secret.lock"), Err: syscall.Errno(3)}})
	raw, err := encodeRedacted(r, current)
	if err != nil || !json.Valid(raw) {
		t.Fatalf("invalid report: %v", err)
	}
	if strings.Contains(string(raw), "probe-private-user") || strings.Contains(string(raw), current.Uid) {
		t.Fatal("identity was not redacted in nested report")
	}
	if _, err := os.Lstat(home); !os.IsNotExist(err) {
		t.Fatalf("collection created home/locks: %v", err)
	}
}

func TestRunRefusesExistingReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(path, time.Second, nil); err == nil {
		t.Fatal("overwrote existing report")
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "preserve" {
		t.Fatalf("changed report: %v", err)
	}
}

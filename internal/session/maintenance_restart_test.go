package session

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPurgeFilesystemCrashRestart(t *testing.T) {
	for _, phase := range []string{"before-tombstone", "after-tombstone", "before-rename", "after-rename", "after-content-removal", "after-cleanup"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{"victim", ".query-cache/victim"} {
				if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, dir, "body"), []byte("retained history"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			run := func(point string, wantCrash bool) {
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPurgeFilesystemCrashHelper$")
				cmd.Env = append(os.Environ(), "REASONIX_PURGE_TEST_ROOT="+root, "REASONIX_PURGE_TEST_POINT="+point)
				output, err := cmd.CombinedOutput()
				var exit *exec.ExitError
				if wantCrash {
					if !errors.As(err, &exit) || exit.ExitCode() != 23 {
						t.Fatalf("crash not reached: %v %s", err, output)
					}
				} else if err != nil {
					t.Fatalf("restart: %v %s", err, output)
				}
			}
			run(phase, true)
			_, markerErr := os.Stat(filepath.Join(root, "tombstone"))
			if phase == "before-tombstone" {
				if !os.IsNotExist(markerErr) {
					t.Fatalf("premature tombstone: %v", markerErr)
				}
				if body, err := os.ReadFile(filepath.Join(root, "victim", "body")); err != nil || string(body) != "retained history" {
					t.Fatalf("lost precommit body: %v", err)
				}
			} else if markerErr != nil {
				t.Fatalf("lost tombstone: %v", markerErr)
			}
			run("", false)
			run("", false)
			for _, path := range []string{"victim", ".purging/victim", ".purging/victim.receipt", ".query-cache/victim"} {
				if _, err := os.Lstat(filepath.Join(root, path)); !os.IsNotExist(err) {
					t.Fatalf("cleanup left %s: %v", path, err)
				}
			}
		})
	}
}

func TestPurgeFilesystemCrashHelper(t *testing.T) {
	root := os.Getenv("REASONIX_PURGE_TEST_ROOT")
	if root == "" {
		return
	}
	point := os.Getenv("REASONIX_PURGE_TEST_POINT")
	err := NewFilesystemPersistence(root).purgeWithTombstone(t.Context(), "victim", func() error {
		return os.WriteFile(filepath.Join(root, "tombstone"), []byte("deleted"), 0600)
	}, func(phase string) {
		if phase == point {
			os.Exit(23)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestExportPublicationRecoveryPreservesReplacedFiles(t *testing.T) {
	parent := t.TempDir()
	dir, err := os.MkdirTemp(parent, ".reasonix-export-batch-")
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"page-000000", "page-000001"} {
		witness := filepath.Join(dir, name)
		if err = os.WriteFile(witness, []byte("staged"), 0600); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(parent, []string{"first.png", "second.png"}[i])
		if err = os.Link(witness, target); err != nil {
			t.Fatal(err)
		}
	}
	if err = writeExportPublication(dir, exportPublication{Targets: []string{"first.png", "second.png"}}); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(parent, "second.png")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(parent, "second.png"), []byte("user replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = recoverExportPublications(parent); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(parent, "first.png")); !os.IsNotExist(err) {
		t.Fatal("partial output survived recovery")
	}
	value, err := os.ReadFile(filepath.Join(parent, "second.png"))
	if err != nil || string(value) != "user replacement" {
		t.Fatal("recovery removed an unrelated replacement")
	}
}
func TestExportPublicationConflictLeavesExistingFile(t *testing.T) {
	parent, source := t.TempDir(), t.TempDir()
	for _, name := range []string{"page-000000", "page-000001"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	targets := []string{filepath.Join(parent, "first.png"), filepath.Join(parent, "second.png")}
	if err := os.WriteFile(targets[1], []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := publishJournaledImages(context.Background(), source, targets); err == nil {
		t.Fatal("overwrote existing output")
	}
	if _, err := os.Stat(targets[0]); !os.IsNotExist(err) {
		t.Fatal("published partial output")
	}
	if err := os.Remove(targets[1]); err != nil {
		t.Fatal(err)
	}
	if err := publishJournaledImages(context.Background(), source, targets); err != nil {
		t.Fatal(err)
	}
	if err := recoverExportPublications(parent); err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if _, err := os.Stat(target); err != nil {
			t.Fatal(err)
		}
	}
}

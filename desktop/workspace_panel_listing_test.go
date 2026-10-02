package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWorkspacePanelListsGenericTopLevelDirectories(t *testing.T) {
	base := t.TempDir()
	for _, dir := range []string{"tmp", "bin", "stage", "src", "node_modules", "dist"} {
		if err := os.MkdirAll(filepath.Join(base, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "main.py"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	listed := map[string]bool{}
	for _, e := range listDirForWorkspaceTarget(base, nil, "") {
		listed[e.Name] = true
	}
	for _, want := range []string{"tmp", "bin", "stage", "src", "main.py"} {
		if !listed[want] {
			t.Errorf("file panel hides %q, a real workspace entry; listed %v", want, listed)
		}
	}
	for _, hidden := range []string{"node_modules", "dist"} {
		if listed[hidden] {
			t.Errorf("file panel lists %q, which stays hidden as vendor/build output", hidden)
		}
	}
}

func TestWorkspaceWatchSeesChangesUnderGenericTopLevelDirectories(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"tmp/notes.py", "bin/run.sh", "stage/plan.md"} {
		if workspaceWatchPathSkipped(root, filepath.Join(root, filepath.FromSlash(rel))) {
			t.Errorf("a change to %s is dropped although the file panel shows it", rel)
		}
	}
	if !workspaceWatchPathSkipped(root, filepath.Join(root, "node_modules", "x", "index.js")) {
		t.Error("changes under node_modules must stay ignored")
	}
}

// A link is listed as what it points at while that stays inside the
// workspace. One that leaves it, dangles, or loops back onto a folder it sits
// in is not offered, and cannot be opened or previewed by naming it.
func TestWorkspacePanelListsOnlyLinksInsideTheTree(t *testing.T) {
	orig, _ := os.Getwd()
	defer os.Chdir(orig)
	base := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{filepath.Join(base, "docs", "a.md"), filepath.Join(elsewhere, "shared.md")} {
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	links := map[string]string{
		"docs-link":                 filepath.Join(base, "docs"),
		"notes.md":                  filepath.Join(base, "docs", "a.md"),
		"out-link":                  elsewhere,
		"out.md":                    filepath.Join(elsewhere, "shared.md"),
		"dangling":                  filepath.Join(base, "gone"),
		"loop":                      ".",
		filepath.Join("docs", "up"): "..",
	}
	if runtime.GOOS != "windows" {
		links["etc"] = "/etc"
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(base, name)); err != nil {
			t.Skipf("cannot create a link here: %v", err)
		}
	}
	if err := os.Chdir(base); err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{}
	for _, e := range (&App{}).ListDir("") {
		got[e.Name] = e.IsDir
	}
	if isDir, ok := got["docs-link"]; !ok || !isDir {
		t.Errorf("linked folder listed = %v, dir = %v; want a folder (%v)", ok, isDir, got)
	}
	if isDir, ok := got["notes.md"]; !ok || isDir {
		t.Errorf("linked file listed = %v, dir = %v; want a file (%v)", ok, isDir, got)
	}
	for _, name := range []string{"out-link", "out.md", "etc", "dangling", "loop"} {
		if _, ok := got[name]; ok {
			t.Errorf("%s is listed: %v", name, got)
		}
	}
	inner := (&App{}).ListDir("docs-link")
	if len(inner) != 1 || inner[0].Name != "a.md" {
		t.Errorf("opening the linked folder lists %v, want a.md", inner)
	}
	for _, e := range (&App{}).ListDir("docs") {
		if e.Name == "up" {
			t.Errorf("docs lists up, a link back onto its own parent")
		}
	}
	for _, rel := range []string{"out-link", "etc"} {
		if entries := (&App{}).ListDir(rel); len(entries) != 0 {
			t.Errorf("ListDir(%q) = %d entries from outside the workspace", rel, len(entries))
		}
	}
	if preview := (&App{}).ReadFile("notes.md"); preview.Err != "" {
		t.Errorf("preview of an in-tree link failed: %s", preview.Err)
	}
	for _, rel := range []string{"out.md", "out-link/shared.md", "etc/hosts"} {
		if preview := (&App{}).ReadFile(rel); preview.Err == "" {
			t.Errorf("ReadFile(%q) previewed a file outside the workspace", rel)
		}
	}
}

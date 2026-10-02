package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/pathidentity"
)

// Every sidebar read loads the projects file, and on Windows one resolution
// opens every component of the path, so the load must resolve each root a
// bounded number of times rather than once per pair of roots.
func TestLoadProjectsFileResolvesEachRootLinearly(t *testing.T) {
	isolateDesktopUserDirs(t)
	base := t.TempDir()
	const projects = 24
	var file desktopProjectFile
	for i := range projects {
		root := filepath.Join(base, fmt.Sprintf("project-%02d", i))
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		file.Projects = append(file.Projects, desktopProject{Root: root, ManualTopicOrder: true, Topics: []string{"t"}})
		file.SidebarOrder = append(file.SidebarOrder, root)
		if i%3 == 0 {
			file.PinnedProjects = append(file.PinnedProjects, root)
		}
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(file.Projects[5].Root, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	file.Projects = append(file.Projects, desktopProject{Root: alias, Title: "Alias title"})
	if err := saveProjectsFile(file); err != nil {
		t.Fatal(err)
	}

	resolutions := 0
	original := resolveDesktopPathIdentity
	resolveDesktopPathIdentity = func(path string, options pathidentity.Options) (pathidentity.Identity, error) {
		resolutions++
		return original(path, options)
	}
	t.Cleanup(func() { resolveDesktopPathIdentity = original })

	loaded := loadProjectsFile()
	if len(loaded.Projects) != projects {
		t.Fatalf("projects = %d, want %d with the alias folded into its target", len(loaded.Projects), projects)
	}
	if loaded.Projects[5].Title != "Alias title" {
		t.Fatalf("alias metadata was not merged into its target: %+v", loaded.Projects[5])
	}
	if len(loaded.SidebarOrder) != projects || len(loaded.PinnedProjects) != (projects+2)/3 {
		t.Fatalf("sidebar order %d / pinned %d changed", len(loaded.SidebarOrder), len(loaded.PinnedProjects))
	}
	if limit := 4 * (projects + 1); resolutions > limit {
		t.Fatalf("loading %d projects resolved path identity %d times, want at most %d", projects, resolutions, limit)
	}
}

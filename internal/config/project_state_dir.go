package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"reasonix/internal/fileutil"
)

const projectCollisionMarker = ".workspace-root"

func projectStateRoot(workspaceRoot string) string {
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		return ""
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	if runtimeGOOS == "windows" {
		root = strings.ToLower(root)
	}
	return root
}

func collisionProjectDir(userDir, root string) string {
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(userDir, "projects", "@"+hex.EncodeToString(sum[:]))
}

// ProjectStateDir preserves the historical directory unless an explicit
// collision assignment was recorded when a project was added. Reads never
// claim a legacy directory or move existing state.
func ProjectStateDir(userDir, workspaceRoot string) string {
	root := projectStateRoot(workspaceRoot)
	if userDir == "" || root == "" {
		return ""
	}
	legacy := filepath.Join(userDir, "projects", WorkspaceSlug(root))
	assigned := collisionProjectDir(userDir, root)
	owner, err := os.ReadFile(filepath.Join(assigned, projectCollisionMarker))
	if err == nil && string(owner) == root+"\n" {
		return assigned
	}
	return legacy
}

// AssignProjectStateCollision records a new project's alternate directory.
// Callers must first establish a slug collision from their saved project list;
// this write is never made by ProjectStateDir or a read-only listing.
func AssignProjectStateCollision(userDir, workspaceRoot string) error {
	root := projectStateRoot(workspaceRoot)
	if userDir == "" || root == "" {
		return fmt.Errorf("project state collision: missing state or workspace root")
	}
	dir := collisionProjectDir(userDir, root)
	marker := filepath.Join(dir, projectCollisionMarker)
	owner, err := os.ReadFile(marker)
	if err == nil {
		if string(owner) == root+"\n" {
			return nil
		}
		return fmt.Errorf("project state collision: %s belongs to another workspace", dir)
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("project state collision: read assignment: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("project state collision: create directory: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("project state collision: inspect directory: %w", err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("project state collision: unowned directory %s is not empty", dir)
	}
	if err := fileutil.AtomicWriteFileStrict(marker, []byte(root+"\n"), 0o600); err != nil {
		return fmt.Errorf("project state collision: record assignment: %w", err)
	}
	return nil
}

package main

import (
	"errors"
	"reasonix/internal/config"
)

var errProjectStateCollisionAssignment = errors.New("project state collision assignment")

// assignAddedProjectStateCollisions assigns a new root only when a recorded
// project still uses the same legacy directory. Recorded roots and existing
// assignments keep their current directory.
func assignAddedProjectStateCollisions(previous []string, projects []desktopProject) error {
	known := append([]string(nil), previous...)
	userDir := config.MemoryUserDir()
	for _, project := range projects {
		root := normalizeProjectRoot(project.Root)
		if root == "" {
			continue
		}
		alreadyKnown := false
		collision := false
		for _, prior := range known {
			if sameProjectRoot(prior, root) {
				alreadyKnown = true
				break
			}
			if config.WorkspaceSlug(prior) == config.WorkspaceSlug(root) &&
				config.ProjectStateDir(userDir, prior) == config.ProjectStateDir(userDir, root) {
				collision = true
			}
		}
		if alreadyKnown {
			continue
		}
		if collision {
			if err := config.AssignProjectStateCollision(userDir, root); err != nil {
				return err
			}
		}
		known = append(known, root)
	}
	return nil
}

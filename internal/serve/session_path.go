package serve

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"reasonix/internal/agent"
	"reasonix/internal/pathidentity"
	"reasonix/internal/store"
)

// resolveSessionPath validates a client-supplied transcript against the
// configured session directory and returns its symlink-resolved access path.
func (s *Server) resolveSessionPath(raw string) (string, error) {
	dir := s.ctl().SessionDir()
	if dir == "" {
		return "", errors.New("sessions disabled")
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", errors.New("invalid session dir")
	}
	dirIdentity, err := pathidentity.Resolve(absDir, pathidentity.Options{FollowLeaf: true})
	if err != nil {
		return "", errors.New("invalid session dir")
	}
	absPath, err := filepath.Abs(strings.TrimSpace(raw))
	if err != nil || !store.IsSessionTranscriptName(filepath.Base(absPath)) {
		return "", errors.New("invalid session path")
	}
	pathIdentity, err := pathidentity.Resolve(absPath, pathidentity.Options{FollowLeaf: true})
	if err != nil {
		return "", errors.New("invalid session path")
	}
	rel, err := filepath.Rel(dirIdentity.Key, pathIdentity.Key)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", errors.New("path outside session dir")
	}
	realPath := pathIdentity.PhysicalPath
	if agent.IsCleanupPending(realPath) {
		return "", errors.New("session is pending cleanup")
	}
	return realPath, nil
}

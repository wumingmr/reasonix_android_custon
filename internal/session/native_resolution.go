package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"reasonix/internal/agent"
)

// ExistingCanonicalForLegacy uses a header lookup for a completed cutover.
// Older paired stores require a read-only prefix proof before choosing either
// source, since either transcript may contain work absent from the other.
// Missing candidates are not created, imported, repaired, or resumed here.
func (s *Service) ExistingCanonicalForLegacy(path string, legacy *agent.Session) (SessionRef, bool, error) {
	filesystem, ok := s.persistence.(*FilesystemPersistence)
	if !ok {
		return SessionRef{}, false, nil
	}
	id := agent.BranchID(path)
	dir, err := filesystem.sessionDir(id, true)
	if errors.Is(err, ErrSessionNotFound) || os.IsNotExist(err) {
		return SessionRef{}, false, nil
	}
	if err != nil {
		return SessionRef{}, false, err
	}
	manifest, err := readStoredManifest(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return SessionRef{}, false, err
	}
	if manifest.SessionID != id {
		return SessionRef{}, false, ErrDamagedStore
	}
	if head, dag := legacy.Head(); dag {
		// A path-derived store is not proof of ownership of every DAG head.
		// Explicit source mappings are resolved by the host before this probe.
		if manifest.Source != nil && manifest.Source.LegacyHeadID != "" {
			if manifest.Source.LegacyHeadID != head.HeadID {
				return SessionRef{}, false, nil
			}
		} else if head.HeadID != agent.SessionMainHead {
			return SessionRef{}, false, nil
		}
	}
	if manifest.Codec != Codec {
		ref := SessionRef{HostID: s.HostID(), SessionID: id}
		history, err := s.Query().History(context.Background(), ref)
		if err != nil {
			return SessionRef{}, false, err
		}
		if len(comparableImportMessages(history)) == 0 {
			return SessionRef{}, false, nil
		}
		if MigrationHistoryContains(history, legacy.Snapshot()) {
			return ref, true, nil
		}
		if MigrationHistoryContains(legacy.Snapshot(), history) {
			return SessionRef{}, false, nil
		}
		return SessionRef{}, false, ErrImportConflict
	}
	return SessionRef{HostID: s.HostID(), SessionID: id}, true, nil
}

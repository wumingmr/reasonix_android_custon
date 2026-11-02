package session

import (
	"context"
	"path/filepath"

	"reasonix/internal/projectiondb"
)

// A projection belongs to one canonical identity below the host's cache root.
// Reject invalid IDs here as well as at query admission: taking their basename
// would silently alias another session or escape the cache for "." and "..".
func sessionProjectionPath(root, sessionID, filename string) string {
	if !filepath.IsLocal(sessionID) || validateSessionID(sessionID) != nil {
		return ""
	}
	return filepath.Join(root, ".query-cache", sessionID, filename)
}

// Session read indexes are disposable projections. Every read entry point
// shares the same verified-file policy; authoritative stores keep full audits.
func openHistoryProjection(ctx context.Context, path string) (*projectiondb.Handle, error) {
	return projectiondb.Open(ctx, projectiondb.OpenOptions{Path: path, Migrations: historyMigrations,
		RequireDisk: true, MaxOpenConns: 1, ReuseIntegrityCheck: true})
}

func openSearchProjection(ctx context.Context, path string) (*projectiondb.Handle, error) {
	return projectiondb.Open(ctx, projectiondb.OpenOptions{Path: path, Migrations: searchMigrations,
		RequireDisk: true, MaxOpenConns: 1, ReuseIntegrityCheck: true})
}

package sessioncatalog

import (
	"context"
	"reasonix/internal/projectiondb"
)

func openCatalogProjection(ctx context.Context, opts Options) (*projectiondb.Handle, error) {
	openProjection := projectiondb.Open
	if opts.DeferredMetadataIntegrity {
		openProjection = projectiondb.OpenAdvisory
	}
	return openProjection(ctx, projectiondb.OpenOptions{
		Path:         opts.Path,
		MemoryName:   "session-catalog",
		Migrations:   sessionMigrations(),
		InMemory:     opts.InMemory,
		MaxOpenConns: 4,
		Now:          opts.Now,
	})
}

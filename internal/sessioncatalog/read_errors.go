package sessioncatalog

import (
	"context"
	"database/sql"
)

// Observe corruption at the shared metadata reader, including Scan/Err where
// SQLite can report a damaged page after QueryContext has already succeeded.
type catalogReader struct {
	owner  *Catalog
	source queryReader
}
type catalogRow struct {
	owner *Catalog
	row   *sql.Row
	err   error
}
type catalogRows struct {
	*sql.Rows
	owner *Catalog
}

func (r catalogReader) QueryRowContext(ctx context.Context, query string, args ...any) *catalogRow {
	if err := r.owner.readable(); err != nil {
		return &catalogRow{owner: r.owner, err: err}
	}
	return &catalogRow{owner: r.owner, row: r.source.QueryRowContext(ctx, query, args...)}
}

func (r *catalogRow) Scan(dest ...any) error {
	if err := r.owner.readable(); err != nil {
		return err
	}
	if r.err != nil {
		return r.err
	}
	err := r.row.Scan(dest...)
	r.owner.observeDatabaseError(err)
	return err
}

func (r catalogReader) QueryContext(ctx context.Context, query string, args ...any) (*catalogRows, error) {
	if err := r.owner.readable(); err != nil {
		return nil, err
	}
	rows, err := r.source.QueryContext(ctx, query, args...)
	if err != nil {
		r.owner.observeDatabaseError(err)
		return nil, err
	}
	return &catalogRows{rows, r.owner}, nil
}

func (r *catalogRows) Scan(dest ...any) error {
	if err := r.owner.readable(); err != nil {
		return err
	}
	err := r.Rows.Scan(dest...)
	r.owner.observeDatabaseError(err)
	return err
}

func (r *catalogRows) Err() error {
	if err := r.owner.readable(); err != nil {
		return err
	}
	err := r.Rows.Err()
	r.owner.observeDatabaseError(err)
	return err
}

func (r *catalogRows) Next() bool {
	return r.owner.readable() == nil && r.Rows.Next()
}

func (r *catalogRows) Close() error {
	err := r.Rows.Close()
	r.owner.observeDatabaseError(err)
	return err
}

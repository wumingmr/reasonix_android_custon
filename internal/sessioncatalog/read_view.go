package sessioncatalog

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"sync"
	"time"

	"reasonix/internal/sqliteuri"
)

type readViewKey struct{}
type readView struct {
	owner    *Catalog
	tx       *sql.Tx
	revision uint64
	now      time.Time
}
type queryReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// ReadLease retains an immutable WAL view for bounded, demand-driven paging.
// It uses a dedicated read-only connection so a long-lived UI cursor cannot
// exhaust the catalog writer's pool. The owner must release it with its cursor.
type ReadLease struct {
	view   *readView
	db     *sql.DB
	cancel context.CancelFunc
	stop   func() bool
	once   sync.Once
}

var ErrReadLeaseUnavailable = errors.New("persistent catalog read lease unavailable")

func (c *Catalog) OpenReadLease(ctx context.Context) (_ *ReadLease, result error) {
	defer func() { c.observeDatabaseError(result) }()
	if err := c.readable(); err != nil {
		return nil, err
	}
	status := c.Status()
	if status.Mode != ModeDisk || status.Path == "" {
		return nil, ErrReadLeaseUnavailable
	}
	dsn, err := sqliteuri.Disk(status.Path, url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(150)"}})
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	lifetime, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.workerCtx, cancel)
	tx, err := db.BeginTx(lifetime, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		stop()
		cancel()
		db.Close()
		return nil, err
	}
	var revision uint64
	if err := tx.QueryRowContext(lifetime, `SELECT revision FROM catalog_state WHERE id=1`).Scan(&revision); err != nil {
		_ = tx.Rollback()
		stop()
		cancel()
		db.Close()
		return nil, err
	}
	lease := &ReadLease{view: &readView{c, tx, revision, c.opts.Now()}, db: db, cancel: cancel, stop: stop}
	if !c.registerReadLease(lease) {
		lease.Close()
		return nil, ErrCatalogInvalidated
	}
	return lease, nil
}

func (l *ReadLease) Context(ctx context.Context) context.Context {
	return context.WithValue(ctx, readViewKey{}, l.view)
}

func (l *ReadLease) Close() {
	l.once.Do(func() {
		l.stop()
		l.cancel()
		_ = l.view.tx.Rollback()
		l.db.Close()
		c := l.view.owner
		c.readLeasesMu.Lock()
		delete(c.readLeases, l)
		c.readLeasesMu.Unlock()
	})
}

// WithReadView pins all nested list reads to one SQLite read transaction. No
// writer mutex is held and callers must finish materialization before return.
func (c *Catalog) WithReadView(ctx context.Context, visit func(context.Context) error) error {
	if v, _ := ctx.Value(readViewKey{}).(*readView); v != nil && v.owner == c {
		return visit(ctx)
	}
	tx, err := c.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// The first read establishes the WAL view, even for an empty result.
	var revision uint64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM catalog_state WHERE id=1`).Scan(&revision); err != nil {
		return err
	}
	v := &readView{c, tx, revision, c.opts.Now()}
	return visit(context.WithValue(ctx, readViewKey{}, v))
}

func (c *Catalog) readDB(ctx context.Context) catalogReader {
	if v, _ := ctx.Value(readViewKey{}).(*readView); v != nil && v.owner == c {
		return catalogReader{c, v.tx}
	}
	return catalogReader{c, c.db}
}
func (c *Catalog) readRevision(ctx context.Context) uint64 {
	if v, _ := ctx.Value(readViewKey{}).(*readView); v != nil && v.owner == c {
		return v.revision
	}
	return c.revision.Load()
}
func (c *Catalog) readTime(ctx context.Context) time.Time {
	if v, _ := ctx.Value(readViewKey{}).(*readView); v != nil && v.owner == c {
		return v.now
	}
	return c.opts.Now()
}

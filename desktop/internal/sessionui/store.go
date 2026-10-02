// Package sessionui owns recoverable Desktop editor state, never chat history.
package sessionui

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"reasonix/internal/sqliteuri"

	_ "modernc.org/sqlite"
)

var ErrConflict = errors.New("session UI revision conflict")
var ErrFutureVersion = errors.New("unsupported session UI schema")

type Record struct {
	Key      string          `json:"key"`
	Revision string          `json:"revision"`
	Payload  json.RawMessage `json:"payload"`
}

type Store struct {
	mu     sync.Mutex
	path   string
	db     *sql.DB
	closed bool
}

func New(path string) *Store { return &Store{path: path} }

func (s *Store) Path() string { return s.path }

// PurgeComposer runs only after canonical deletion has crossed its tombstone.
func (s *Store) PurgeComposer(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err := s.open(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `DELETE FROM records WHERE kind='composer' AND key=?`, key); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM conflicts WHERE kind='composer' AND key=?`, key); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM records WHERE kind='submission' AND substr(key,1,?)=?`, len(key)+1, key+"/"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) open() error {
	if s.closed {
		return errors.New("session UI store is closed")
	}
	if s.db != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	dsn, err := sqliteuri.Disk(s.path, nil)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	ok := false
	defer func() {
		if !ok {
			_ = db.Close()
		}
	}()
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version < 0 || version > 1 {
		return ErrFutureVersion
	}
	if version == 0 {
		var tables int
		if err = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`).Scan(&tables); err != nil {
			return err
		}
		if tables != 0 {
			if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
				return err
			}
			if version != 1 {
				return ErrFutureVersion
			}
		}
	}
	for _, statement := range []string{"PRAGMA busy_timeout = 5000", "PRAGMA journal_mode = WAL"} {
		if _, err = db.Exec(statement); err != nil {
			return err
		}
	}
	if version == 0 {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		for _, statement := range []string{
			`CREATE TABLE IF NOT EXISTS records (kind TEXT NOT NULL, key TEXT NOT NULL, revision INTEGER NOT NULL, payload BLOB NOT NULL, PRIMARY KEY(kind,key))`,
			`CREATE TABLE IF NOT EXISTS conflicts (id INTEGER PRIMARY KEY, kind TEXT NOT NULL, key TEXT NOT NULL, expected INTEGER NOT NULL, actual INTEGER NOT NULL, payload BLOB NOT NULL)`,
			`PRAGMA user_version = 1`,
		} {
			if _, err = tx.Exec(statement); err != nil {
				return err
			}
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	s.db, ok = db, true
	return nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

func scan(row interface{ Scan(...any) error }, key string) (Record, error) {
	r := Record{Key: key, Revision: "0", Payload: json.RawMessage(`{}`)}
	var revision int64
	err := row.Scan(&revision, &r.Payload)
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	r.Revision = strconv.FormatInt(revision, 10)
	return r, nil
}

func (s *Store) Get(ctx context.Context, kind, key string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.open(); err != nil {
		return Record{}, err
	}
	return scan(s.db.QueryRowContext(ctx, `SELECT revision,payload FROM records WHERE kind=? AND key=?`, kind, key), key)
}

// Save uses SQL CAS across processes. A losing editor's complete input is kept
// before returning the winning record; force-overwrite is intentionally absent.
func (s *Store) Save(ctx context.Context, kind, key, expected string, payload json.RawMessage, submissions ...Record) (Record, error) {
	if !json.Valid(payload) || key == "" || kind == "" {
		return Record{}, errors.New("invalid session UI record")
	}
	rev, err := strconv.ParseInt(expected, 10, 64)
	if err != nil || rev < 0 || rev == int64(^uint64(0)>>1) {
		return Record{}, errors.New("invalid session UI revision")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.open(); err != nil {
		return Record{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = tx.Rollback() }()
	// Write first: acquiring SQLite's writer before reading avoids upgrading a
	// stale deferred read transaction when another process saves concurrently.
	var result sql.Result
	if rev == 0 {
		result, err = tx.ExecContext(ctx, `INSERT INTO records(kind,key,revision,payload) VALUES(?,?,1,?) ON CONFLICT(kind,key) DO NOTHING`, kind, key, []byte(payload))
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE records SET revision=revision+1,payload=? WHERE kind=? AND key=? AND revision=?`, []byte(payload), kind, key, rev)
	}
	if err != nil {
		return Record{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return Record{}, err
	}
	r, err := scan(tx.QueryRowContext(ctx, `SELECT revision,payload FROM records WHERE kind=? AND key=?`, kind, key), key)
	if err != nil {
		return r, err
	}
	// Formal composer input has one saved value. A stale writer receives the
	// current revision and may rebase; it does not create alternate input copies.
	if n == 0 && kind != "composer" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO conflicts(kind,key,expected,actual,payload) VALUES(?,?,?,?,?)`, kind, key, rev, r.Revision, []byte(payload)); err != nil {
			return r, err
		}
	}
	if n != 0 {
		for _, submission := range submissions {
			if !json.Valid(submission.Payload) {
				return r, errors.New("invalid submission record")
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO records(kind,key,revision,payload) VALUES('submission',?,1,?) ON CONFLICT(kind,key) DO UPDATE SET revision=revision+1,payload=excluded.payload`, submission.Key, []byte(submission.Payload)); err != nil {
				return r, err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return r, err
	}
	if n == 0 {
		return r, ErrConflict
	}
	return r, nil
}

func (s *Store) List(ctx context.Context, kind string) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.open(); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT key,revision,payload FROM records WHERE kind=? ORDER BY key`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		var r Record
		var rev int64
		if err := rows.Scan(&r.Key, &rev, &r.Payload); err != nil {
			return nil, err
		}
		r.Revision = fmt.Sprint(rev)
		out = append(out, r)
	}
	return out, rows.Err()
}

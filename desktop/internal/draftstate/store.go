package draftstate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	sqlite "modernc.org/sqlite"
)

const SchemaVersion = 4
const SnapshotVersion = 5

var (
	ErrConflict           = errors.New("session draft revision conflict")
	ErrNotFound           = errors.New("session draft not found")
	ErrConverted          = errors.New("session draft was converted")
	ErrOperationConflict  = errors.New("session draft submission conflicts with the active operation")
	ErrOperationNotFound  = errors.New("session draft submission not found")
	ErrUnsupportedVersion = errors.New("session draft schema version is unsupported")
)

type Draft struct {
	ID            string
	WorkspaceID   string
	Scope         string
	WorkspaceRoot string
	Revision      uint64
	ContentJSON   string
	SettingsJSON  string
	Status        string
	UpdatedAt     time.Time
}

type Operation struct {
	RequestID     string
	SourceDigest  string
	Revision      uint64
	ExecutionJSON string
	ID            string
	DraftID       string
	WorkspaceID   string
	DraftRevision uint64
	SessionID     string
	TopicID       string
	SubmissionID  string
	Fingerprint   string
	RequestJSON   string
	Phase         string
	Error         string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type Store struct {
	path string
	mu   sync.Mutex
	db   *sql.DB
}

func New(path string) *Store { return &Store{path: filepath.Clean(path)} }

func (s *Store) Path() string {
	if s == nil || s.path == "." {
		return ""
	}
	return s.path
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

func (s *Store) openLocked() error {
	if s.db != nil {
		return nil
	}
	if s == nil || s.path == "" || s.path == "." {
		return errors.New("session draft database path is unavailable")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", draftFileDSN(s.path))
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	var version int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		_ = db.Close()
		return err
	}
	if version < 0 || version > SchemaVersion {
		_ = db.Close()
		return fmt.Errorf("%w: %d", ErrUnsupportedVersion, version)
	}
	if version == 0 {
		var tables int
		if err = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`).Scan(&tables); err != nil {
			_ = db.Close()
			return err
		}
		if tables != 0 {
			// A concurrent first opener may have committed the schema after our
			// first read. Only an identifiable committed format may be opened.
			if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
				_ = db.Close()
				return err
			}
			if version < 1 || version > SchemaVersion {
				_ = db.Close()
				return fmt.Errorf("%w: %d", ErrUnsupportedVersion, version)
			}
		}
	}
	if version > 0 && version < SchemaVersion {
		if err := backupBeforeUpgrade(db, s.path, version); err != nil {
			_ = db.Close()
			return err
		}
	}
	// Even changing journal mode writes the database header. Check the version
	// first so an older binary leaves an unknown future file byte-for-byte intact.
	for _, statement := range []string{
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA journal_mode = WAL`,
		`PRAGMA foreign_keys = ON`,
	} {
		if _, err = db.Exec(statement); err != nil {
			_ = db.Close()
			return err
		}
	}
	if version == 0 {
		if err = initialize(db); err != nil {
			_ = db.Close()
			return err
		}
	} else {
		if version == 1 {
			if err = migrateV1ToV2(db); err != nil {
				_ = db.Close()
				return err
			}
			version = 2
		}
		if version == 2 {
			if err = migrateV2ToV3(db); err != nil {
				_ = db.Close()
				return err
			}
			version = 3
		}
		if version == 3 {
			if err = migrateV3ToV4(db); err != nil {
				_ = db.Close()
				return err
			}
		}
	}
	s.db = db
	return nil
}

func initialize(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer rollbackTransaction(tx)
	statements := []string{
		`CREATE TABLE IF NOT EXISTS drafts (
			id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, scope TEXT NOT NULL,
			workspace_root TEXT NOT NULL, revision INTEGER NOT NULL,
			content_json TEXT NOT NULL, settings_json TEXT NOT NULL,
			status TEXT NOT NULL, updated_at INTEGER NOT NULL
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS one_active_draft_per_workspace ON drafts(workspace_id) WHERE status = 'active'`,
		`CREATE TABLE IF NOT EXISTS operations (
			id TEXT PRIMARY KEY, draft_id TEXT NOT NULL, workspace_id TEXT NOT NULL,
			draft_revision INTEGER NOT NULL, session_id TEXT NOT NULL,
			topic_id TEXT NOT NULL,
			submission_id TEXT NOT NULL, fingerprint TEXT NOT NULL,
			request_json TEXT NOT NULL, phase TEXT NOT NULL, error TEXT NOT NULL,
			created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
			request_id TEXT NOT NULL DEFAULT '', source_digest TEXT NOT NULL DEFAULT '',
			operation_revision INTEGER NOT NULL DEFAULT 1, execution_json TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS operations_by_draft ON operations(draft_id, updated_at DESC)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS operations_by_request ON operations(draft_id, request_id) WHERE request_id <> ''`,
		`CREATE TABLE IF NOT EXISTS conflicts (
			id INTEGER PRIMARY KEY AUTOINCREMENT, draft_id TEXT NOT NULL,
			expected_revision INTEGER NOT NULL, actual_revision INTEGER NOT NULL,
			content_json TEXT NOT NULL, settings_json TEXT NOT NULL, created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS restore_state (slot TEXT PRIMARY KEY, draft_id TEXT NOT NULL, updated_at INTEGER NOT NULL)`,
		fmt.Sprintf(`PRAGMA user_version = %d`, SchemaVersion),
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func migrateV3ToV4(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer rollbackTransaction(tx)
	for _, query := range []string{
		`ALTER TABLE operations ADD COLUMN request_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE operations ADD COLUMN source_digest TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE operations ADD COLUMN operation_revision INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE operations ADD COLUMN execution_json TEXT NOT NULL DEFAULT ''`,
		`CREATE UNIQUE INDEX operations_by_request ON operations(draft_id, request_id) WHERE request_id <> ''`,
		`PRAGMA user_version = 4`,
	} {
		if _, err := tx.Exec(query); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func migrateV1ToV2(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(`ALTER TABLE operations ADD COLUMN topic_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if _, err := tx.Exec(`PRAGMA user_version = 2`); err != nil {
		return err
	}
	return tx.Commit()
}

// v3 makes operation request_json a versioned, frozen execution snapshot.
// The shape is validated by the application because keeping the JSON opaque
// lets older completed records remain readable without rewriting user data.
func migrateV2ToV3(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(`PRAGMA user_version = 3`); err != nil {
		return err
	}
	return tx.Commit()
}

func draftFileDSN(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	slash := filepath.ToSlash(abs)
	if runtime.GOOS == "windows" && len(slash) >= 2 && slash[1] == ':' {
		slash = "/" + slash
	}
	u := &url.URL{Scheme: "file", Path: slash}
	return u.String() + "?_pragma=busy_timeout%285000%29&_pragma=foreign_keys%281%29"
}

func (s *Store) withDB(ctx context.Context, fn func(*sql.DB) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	backoff := 5 * time.Millisecond
	for {
		if err := s.openLocked(); err != nil {
			if !isSQLiteBusy(err) {
				return err
			}
		} else if err := fn(s.db); err != nil {
			if !isSQLiteBusy(err) {
				return err
			}
		} else {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("session draft database remained busy for 5s")
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if backoff < 100*time.Millisecond {
			backoff *= 2
		}
	}
}

func isSQLiteBusy(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	code := sqliteErr.Code() & 0xff
	return code == 5 || code == 6
}

func scanDraft(row interface{ Scan(...any) error }) (Draft, error) {
	var draft Draft
	var updated int64
	err := row.Scan(&draft.ID, &draft.WorkspaceID, &draft.Scope, &draft.WorkspaceRoot,
		&draft.Revision, &draft.ContentJSON, &draft.SettingsJSON, &draft.Status, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Draft{}, ErrNotFound
	}
	draft.UpdatedAt = time.UnixMilli(updated).UTC()
	return draft, err
}

const draftColumns = `id, workspace_id, scope, workspace_root, revision, content_json, settings_json, status, updated_at`

func (s *Store) Open(ctx context.Context, workspaceID, scope, root, draftID, settings string) (Draft, bool, error) {
	var result Draft
	created := false
	err := s.withDB(ctx, func(db *sql.DB) error {
		current, err := scanDraft(db.QueryRowContext(ctx, `SELECT `+draftColumns+` FROM drafts WHERE workspace_id = ? AND status = 'active'`, workspaceID))
		if err == nil {
			result = current
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		now := time.Now().UTC().UnixMilli()
		_, err = db.ExecContext(ctx, `INSERT INTO drafts(id, workspace_id, scope, workspace_root, revision, content_json, settings_json, status, updated_at) VALUES(?,?,?,?,1,'{}',?,'active',?)`, draftID, workspaceID, scope, root, settings, now)
		if err != nil {
			// A concurrent process may have won the unique active-draft insert.
			current, lookupErr := scanDraft(db.QueryRowContext(ctx, `SELECT `+draftColumns+` FROM drafts WHERE workspace_id = ? AND status = 'active'`, workspaceID))
			if lookupErr != nil {
				return err
			}
			result = current
			return nil
		}
		result = Draft{ID: draftID, WorkspaceID: workspaceID, Scope: scope, WorkspaceRoot: root, Revision: 1, ContentJSON: "{}", SettingsJSON: settings, Status: "active", UpdatedAt: time.UnixMilli(now).UTC()}
		created = true
		return nil
	})
	return result, created, err
}

func (s *Store) Get(ctx context.Context, draftID string) (Draft, error) {
	var result Draft
	err := s.withDB(ctx, func(db *sql.DB) error {
		var err error
		result, err = scanDraft(db.QueryRowContext(ctx, `SELECT `+draftColumns+` FROM drafts WHERE id = ?`, draftID))
		return err
	})
	return result, err
}

func (s *Store) SetRestore(ctx context.Context, draftID string) error {
	return s.withDB(ctx, func(db *sql.DB) error {
		if strings.TrimSpace(draftID) == "" {
			_, err := db.ExecContext(ctx, `DELETE FROM restore_state WHERE slot='active'`)
			return err
		}
		var status string
		if err := db.QueryRowContext(ctx, `SELECT status FROM drafts WHERE id=?`, draftID).Scan(&status); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		} else if status != "active" {
			return ErrConverted
		}
		_, err := db.ExecContext(ctx, `INSERT INTO restore_state(slot,draft_id,updated_at) VALUES('active',?,?) ON CONFLICT(slot) DO UPDATE SET draft_id=excluded.draft_id, updated_at=excluded.updated_at`, draftID, time.Now().UTC().UnixMilli())
		return err
	})
}

func (s *Store) Restore(ctx context.Context) (Draft, error) {
	var result Draft
	err := s.withDB(ctx, func(db *sql.DB) error {
		var err error
		result, err = scanDraft(db.QueryRowContext(ctx, `SELECT `+draftColumns+` FROM drafts WHERE id=(SELECT draft_id FROM restore_state WHERE slot='active') AND status='active'`))
		return err
	})
	return result, err
}

func (s *Store) ClearRestore(ctx context.Context, draftID string) error {
	return s.withDB(ctx, func(db *sql.DB) error {
		_, err := db.ExecContext(ctx, `DELETE FROM restore_state WHERE slot='active' AND draft_id=?`, draftID)
		return err
	})
}

func (s *Store) Save(ctx context.Context, draftID string, expected uint64, content, settings string, _ bool) (Draft, error) {
	var result Draft
	err := s.withDB(ctx, func(db *sql.DB) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer rollbackTransaction(tx)
		current, err := scanDraft(tx.QueryRowContext(ctx, `SELECT `+draftColumns+` FROM drafts WHERE id = ?`, draftID))
		if err != nil {
			return err
		}
		if current.Status != "active" {
			return ErrConverted
		}
		var activeOperations int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM operations WHERE draft_id=? AND phase NOT IN ('cancelled','terminal_failed')`, draftID).Scan(&activeOperations); err != nil {
			return err
		}
		if activeOperations > 0 {
			return ErrOperationConflict
		}
		if current.Revision != expected {
			_, conflictErr := tx.ExecContext(ctx, `INSERT INTO conflicts(draft_id,expected_revision,actual_revision,content_json,settings_json,created_at) VALUES(?,?,?,?,?,?)`, draftID, expected, current.Revision, content, settings, time.Now().UTC().UnixMilli())
			if conflictErr != nil {
				return conflictErr
			}
			if err := tx.Commit(); err != nil {
				return err
			}
			result = current
			return ErrConflict
		}
		now := time.Now().UTC().UnixMilli()
		next := current.Revision + 1
		resultUpdate, err := tx.ExecContext(ctx, `UPDATE drafts SET revision=?,content_json=?,settings_json=?,updated_at=? WHERE id=? AND status='active' AND revision=?`, next, content, settings, now, draftID, current.Revision)
		if err != nil {
			return err
		}
		if changed, err := resultUpdate.RowsAffected(); err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return ErrConflict
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		result = current
		result.Revision = next
		result.ContentJSON = content
		result.SettingsJSON = settings
		result.UpdatedAt = time.UnixMilli(now).UTC()
		return nil
	})
	return result, err
}

func (s *Store) ListActive(ctx context.Context) ([]Draft, error) {
	out := []Draft{}
	err := s.withDB(ctx, func(db *sql.DB) error {
		rows, err := db.QueryContext(ctx, `SELECT `+draftColumns+` FROM drafts WHERE status='active' ORDER BY updated_at DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			draft, err := scanDraft(rows)
			if err != nil {
				return err
			}
			out = append(out, draft)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) Discard(ctx context.Context, draftID string, expected uint64) error {
	return s.withDB(ctx, func(db *sql.DB) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer rollbackTransaction(tx)
		current, err := scanDraft(tx.QueryRowContext(ctx, `SELECT `+draftColumns+` FROM drafts WHERE id=?`, draftID))
		if err != nil {
			return err
		}
		if current.Status != "active" {
			return ErrConverted
		}
		if current.Revision != expected {
			return ErrConflict
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM operations WHERE draft_id=? AND phase NOT IN ('cancelled','terminal_failed')`, draftID).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return ErrOperationConflict
		}
		if _, err = tx.ExecContext(ctx, `UPDATE drafts SET status='discarded',revision=revision+1,updated_at=? WHERE id=?`, time.Now().UTC().UnixMilli(), draftID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM restore_state WHERE slot='active' AND draft_id=?`, draftID); err != nil {
			return err
		}
		return tx.Commit()
	})
}

func (s *Store) BeginOperation(ctx context.Context, op Operation) (Operation, bool, error) {
	var result Operation
	created := false
	err := s.withDB(ctx, func(db *sql.DB) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer rollbackTransaction(tx)
		// Resolve request identity before checking the editable slot: a lost
		// response may be retried after receipt has converted the draft.
		if op.RequestID != "" {
			prior, lookupErr := scanOperation(tx.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM operations WHERE draft_id=? AND request_id=?`, op.DraftID, op.RequestID))
			if lookupErr == nil {
				if prior.Fingerprint != op.Fingerprint || prior.SourceDigest != op.SourceDigest {
					return ErrOperationConflict
				}
				result = prior
				return nil
			}
			if !errors.Is(lookupErr, ErrOperationNotFound) {
				return lookupErr
			}
		}
		draft, err := scanDraft(tx.QueryRowContext(ctx, `SELECT `+draftColumns+` FROM drafts WHERE id=?`, op.DraftID))
		if err != nil {
			return err
		}
		if draft.Status != "active" {
			return ErrConverted
		}
		if draft.Revision != op.DraftRevision {
			return ErrConflict
		}
		if op.SourceDigest != "" {
			digest, err := SnapshotDigest(draft.ContentJSON, draft.SettingsJSON)
			if err != nil {
				return err
			}
			if digest != op.SourceDigest {
				return ErrConflict
			}
		}
		prior, err := scanOperation(tx.QueryRowContext(ctx, `SELECT id,draft_id,workspace_id,draft_revision,session_id,topic_id,submission_id,fingerprint,request_json,phase,error,created_at,updated_at,request_id,source_digest,operation_revision,execution_json FROM operations WHERE draft_id=? ORDER BY rowid DESC LIMIT 1`, op.DraftID))
		if err == nil && prior.Phase != "cancelled" && prior.Phase != "terminal_failed" {
			if prior.Fingerprint != op.Fingerprint {
				return ErrOperationConflict
			}
			result = prior
			return nil
		}
		if err != nil && !errors.Is(err, ErrOperationNotFound) {
			return err
		}
		if err == nil && prior.SessionID != "" {
			op.SessionID = prior.SessionID
			op.TopicID = prior.TopicID
		}
		now := time.Now().UTC()
		op.CreatedAt, op.UpdatedAt, op.Phase, op.Error, op.Revision = now, now, "reserved", "", 1
		_, err = tx.ExecContext(ctx, `INSERT INTO operations(id,draft_id,workspace_id,draft_revision,session_id,topic_id,submission_id,fingerprint,request_json,phase,error,created_at,updated_at,request_id,source_digest,operation_revision) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, op.ID, op.DraftID, op.WorkspaceID, op.DraftRevision, op.SessionID, op.TopicID, op.SubmissionID, op.Fingerprint, op.RequestJSON, op.Phase, op.Error, now.UnixMilli(), now.UnixMilli(), op.RequestID, op.SourceDigest, op.Revision)
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		result, created = op, true
		return nil
	})
	return result, created, err
}

func scanOperation(row interface{ Scan(...any) error }) (Operation, error) {
	var op Operation
	var created, updated int64
	err := row.Scan(&op.ID, &op.DraftID, &op.WorkspaceID, &op.DraftRevision, &op.SessionID, &op.TopicID, &op.SubmissionID, &op.Fingerprint, &op.RequestJSON, &op.Phase, &op.Error, &created, &updated, &op.RequestID, &op.SourceDigest, &op.Revision, &op.ExecutionJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, ErrOperationNotFound
	}
	op.CreatedAt, op.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
	return op, err
}

func (s *Store) Operation(ctx context.Context, id string) (Operation, error) {
	var result Operation
	err := s.withDB(ctx, func(db *sql.DB) error {
		var err error
		result, err = scanOperation(db.QueryRowContext(ctx, `SELECT id,draft_id,workspace_id,draft_revision,session_id,topic_id,submission_id,fingerprint,request_json,phase,error,created_at,updated_at,request_id,source_digest,operation_revision,execution_json FROM operations WHERE id=?`, id))
		return err
	})
	return result, err
}

func (s *Store) PendingOperations(ctx context.Context) ([]Operation, error) {
	out := []Operation{}
	err := s.withDB(ctx, func(db *sql.DB) error {
		rows, err := db.QueryContext(ctx, `SELECT o.id,o.draft_id,o.workspace_id,o.draft_revision,o.session_id,o.topic_id,o.submission_id,o.fingerprint,o.request_json,o.phase,o.error,o.created_at,o.updated_at,o.request_id,o.source_digest,o.operation_revision,o.execution_json FROM operations o JOIN drafts d ON d.id=o.draft_id WHERE d.status='active' AND o.phase NOT IN ('cancelled','terminal_failed') ORDER BY o.created_at`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			op, err := scanOperation(rows)
			if err != nil {
				return err
			}
			out = append(out, op)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) SetOperationPhase(ctx context.Context, id, phase, message string) (Operation, error) {
	var result Operation
	err := s.withDB(ctx, func(db *sql.DB) error {
		now := time.Now().UTC().UnixMilli()
		if _, err := db.ExecContext(ctx, `UPDATE operations SET phase=?,error=?,updated_at=?,operation_revision=operation_revision+1 WHERE id=?`, phase, message, now, id); err != nil {
			return err
		}
		var err error
		result, err = scanOperation(db.QueryRowContext(ctx, `SELECT id,draft_id,workspace_id,draft_revision,session_id,topic_id,submission_id,fingerprint,request_json,phase,error,created_at,updated_at,request_id,source_digest,operation_revision,execution_json FROM operations WHERE id=?`, id))
		return err
	})
	return result, err
}

func (s *Store) ClaimOperationPhase(ctx context.Context, id string, from []string, phase string) (Operation, bool, error) {
	return s.TransitionOperationPhase(ctx, id, from, phase, "")
}

func (s *Store) TransitionOperationPhase(ctx context.Context, id string, from []string, phase, message string) (Operation, bool, error) {
	var result Operation
	claimed := false
	err := s.withDB(ctx, func(db *sql.DB) error {
		if len(from) == 0 {
			return ErrOperationConflict
		}
		placeholders := make([]string, len(from))
		args := make([]any, 0, len(from)+4)
		args = append(args, phase, message, time.Now().UTC().UnixMilli(), id)
		for i, value := range from {
			placeholders[i] = "?"
			args = append(args, value)
		}
		updated, err := db.ExecContext(ctx, `UPDATE operations SET phase=?,error=?,updated_at=?,operation_revision=operation_revision+1 WHERE id=? AND phase IN (`+strings.Join(placeholders, ",")+`)`, args...)
		if err != nil {
			return err
		}
		count, err := updated.RowsAffected()
		if err != nil {
			return err
		}
		claimed = count == 1
		result, err = scanOperation(db.QueryRowContext(ctx, `SELECT id,draft_id,workspace_id,draft_revision,session_id,topic_id,submission_id,fingerprint,request_json,phase,error,created_at,updated_at,request_id,source_digest,operation_revision,execution_json FROM operations WHERE id=?`, id))
		return err
	})
	return result, claimed, err
}

func (s *Store) UpdateOperationRequest(ctx context.Context, id, phase, requestJSON string) (Operation, bool, error) {
	var result Operation
	updated := false
	err := s.withDB(ctx, func(db *sql.DB) error {
		now := time.Now().UTC().UnixMilli()
		change, err := db.ExecContext(ctx, `UPDATE operations SET execution_json=?,updated_at=?,operation_revision=operation_revision+1 WHERE id=? AND phase=?`, requestJSON, now, id, phase)
		if err != nil {
			return err
		}
		count, err := change.RowsAffected()
		if err != nil {
			return err
		}
		updated = count == 1
		result, err = scanOperation(db.QueryRowContext(ctx, `SELECT id,draft_id,workspace_id,draft_revision,session_id,topic_id,submission_id,fingerprint,request_json,phase,error,created_at,updated_at,request_id,source_digest,operation_revision,execution_json FROM operations WHERE id=?`, id))
		return err
	})
	return result, updated, err
}

func (s *Store) EnsureOperationTopic(ctx context.Context, id, topicID string) (Operation, error) {
	var result Operation
	err := s.withDB(ctx, func(db *sql.DB) error {
		if _, err := db.ExecContext(ctx, `UPDATE operations SET topic_id=?,updated_at=?,operation_revision=operation_revision+1 WHERE id=? AND topic_id=''`, topicID, time.Now().UTC().UnixMilli(), id); err != nil {
			return err
		}
		var err error
		result, err = scanOperation(db.QueryRowContext(ctx, `SELECT id,draft_id,workspace_id,draft_revision,session_id,topic_id,submission_id,fingerprint,request_json,phase,error,created_at,updated_at,request_id,source_digest,operation_revision,execution_json FROM operations WHERE id=?`, id))
		return err
	})
	return result, err
}

// AcceptAndConvert publishes the durable submission receipt and releases the
// Workspace draft slot in one database transaction.
func (s *Store) AcceptAndConvert(ctx context.Context, draftID, operationID string) (Operation, error) {
	var result Operation
	err := s.withDB(ctx, func(db *sql.DB) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer rollbackTransaction(tx)
		op, err := scanOperation(tx.QueryRowContext(ctx, `SELECT id,draft_id,workspace_id,draft_revision,session_id,topic_id,submission_id,fingerprint,request_json,phase,error,created_at,updated_at,request_id,source_digest,operation_revision,execution_json FROM operations WHERE id=? AND draft_id=?`, operationID, draftID))
		if err != nil {
			return err
		}
		if op.Phase != "accepted" && op.Phase != "dispatching" && op.Phase != "dispatch_unknown" && op.Phase != "dispatching_shell" {
			return ErrOperationConflict
		}
		now := time.Now().UTC().UnixMilli()
		if op.Phase != "accepted" {
			changed, err := tx.ExecContext(ctx, `UPDATE operations SET phase='accepted',error='',updated_at=?,operation_revision=operation_revision+1 WHERE id=? AND phase=?`, now, operationID, op.Phase)
			if err != nil {
				return err
			}
			op.Revision++
			if count, err := changed.RowsAffected(); err != nil || count != 1 {
				if err != nil {
					return err
				}
				return ErrOperationConflict
			}
		}
		converted, err := tx.ExecContext(ctx, `UPDATE drafts SET status='converted',revision=revision+1,updated_at=? WHERE id=? AND status='active'`, now, draftID)
		if err != nil {
			return err
		}
		if count, err := converted.RowsAffected(); err != nil {
			return err
		} else if count == 0 {
			var status string
			if err := tx.QueryRowContext(ctx, `SELECT status FROM drafts WHERE id=?`, draftID).Scan(&status); err != nil {
				return err
			}
			if status != "converted" {
				return ErrOperationConflict
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM restore_state WHERE slot='active' AND draft_id=?`, draftID); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		op.Phase, op.Error, op.UpdatedAt = "accepted", "", time.UnixMilli(now).UTC()
		result = op
		return nil
	})
	return result, err
}

func (s *Store) Convert(ctx context.Context, draftID, operationID string) error {
	return s.withDB(ctx, func(db *sql.DB) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer rollbackTransaction(tx)
		var phase string
		if err := tx.QueryRowContext(ctx, `SELECT phase FROM operations WHERE id=? AND draft_id=?`, operationID, draftID).Scan(&phase); err != nil {
			return err
		}
		if phase != "accepted" {
			return ErrOperationConflict
		}
		if _, err := tx.ExecContext(ctx, `UPDATE drafts SET status='converted',revision=revision+1,updated_at=? WHERE id=? AND status='active'`, time.Now().UTC().UnixMilli(), draftID); err != nil {
			return err
		}
		return tx.Commit()
	})
}

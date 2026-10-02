package draftstate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	filelock "reasonix/internal/identitylock"
)

const operationColumns = `id,draft_id,workspace_id,draft_revision,session_id,topic_id,submission_id,fingerprint,request_json,phase,error,created_at,updated_at,request_id,source_digest,operation_revision,execution_json`

func rollbackTransaction(tx *sql.Tx) {
	_ = tx.Rollback()
}

// State reads both sides of the draft-operation binding in one transaction.
func (s *Store) State(ctx context.Context, id string) (Draft, *Operation, error) {
	var draft Draft
	var operation *Operation
	err := s.withDB(ctx, func(db *sql.DB) error {
		tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return err
		}
		defer rollbackTransaction(tx)
		draft, err = scanDraft(tx.QueryRowContext(ctx, `SELECT `+draftColumns+` FROM drafts WHERE id=?`, id))
		if err != nil {
			return err
		}
		op, err := scanOperation(tx.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM operations WHERE draft_id=? ORDER BY rowid DESC LIMIT 1`, id))
		if err == nil {
			operation = &op
		} else if !errors.Is(err, ErrOperationNotFound) {
			return err
		}
		return tx.Commit()
	})
	return draft, operation, err
}

func (s *Store) RequestOperation(ctx context.Context, draftID, requestID string) (Operation, error) {
	var op Operation
	err := s.withDB(ctx, func(db *sql.DB) error {
		var err error
		op, err = scanOperation(db.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM operations WHERE draft_id=? AND request_id=? ORDER BY rowid DESC LIMIT 1`, draftID, requestID))
		return err
	})
	return op, err
}

// WorkerLease excludes live creation workers across windows and processes.
// It is separate from the Session writer lease and never waits for its owner.
func (s *Store) WorkerLease(sessionID string) (func(), error) {
	sum := sha256.Sum256([]byte(sessionID))
	directory := filepath.Join(filepath.Dir(s.path), "draft-workers")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	return filelock.TryAcquire(filepath.Join(directory, fmt.Sprintf("%x.lock", sum)))
}

// PublicationLease serializes cancellation with the short compare/publish
// boundary. Controller construction never holds this lock or a DB transaction.
func (s *Store) PublicationLease(ctx context.Context, operationID string) (func(), error) {
	sum := sha256.Sum256([]byte(operationID))
	directory := filepath.Join(filepath.Dir(s.path), "draft-workers")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return filelock.Acquire(bounded, filepath.Join(directory, fmt.Sprintf("%x.publish.lock", sum)))
}

func (s *Store) ResumeOperation(ctx context.Context, id string, revision uint64) (Operation, error) {
	var op Operation
	err := s.withDB(ctx, func(db *sql.DB) error {
		result, err := db.ExecContext(ctx, `UPDATE operations SET phase='reserved',error='',operation_revision=operation_revision+1 WHERE id=? AND operation_revision=? AND phase IN ('resume_required','runtime_failed')`, id, revision)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrOperationConflict
		}
		op, err = scanOperation(db.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM operations WHERE id=?`, id))
		return err
	})
	return op, err
}

package agent

import (
	"context"
	"database/sql"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"os"

	"reasonix/internal/fileops"
	"reasonix/internal/store"
)

func restoreEventPagerScan(ctx context.Context, db *sql.DB, sourceKey string, size int64) (eventPagerScanProgress, error) {
	p := eventPagerScanProgress{Version: 1, SourceKey: sourceKey}
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS event_locations(position INTEGER PRIMARY KEY,offset INTEGER NOT NULL,length INTEGER NOT NULL,at INTEGER NOT NULL);
	CREATE TABLE IF NOT EXISTS event_pending(position INTEGER PRIMARY KEY,offset INTEGER NOT NULL,length INTEGER NOT NULL);`)
	if err != nil {
		return p, err
	}
	var raw string
	err = db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='event_scan_progress'`).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	var saved eventPagerScanProgress
	valid := err == nil && json.Unmarshal([]byte(raw), &saved) == nil && saved.Version == 1 && saved.SourceKey == sourceKey &&
		saved.Offset >= 0 && saved.Offset <= size && saved.Count >= 0 && saved.Pending >= 0 && len(saved.Record.Messages) == 0 &&
		(!saved.InMessages && saved.Pending == 0 || saved.InMessages && !saved.Done && saved.Pending > 0)
	for _, table := range []struct {
		name string
		rows int
	}{{"event_locations", saved.Count}, {"event_pending", saved.Pending}} {
		var count, first, last int
		var end int64
		err := db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MIN(position),0),COALESCE(MAX(position),-1),COALESCE(MAX(offset+length),0) FROM `+table.name).Scan(&count, &first, &last, &end)
		if err != nil {
			return p, err
		}
		valid = valid && count == table.rows && first == 0 && last == count-1 && end <= saved.Offset
	}
	if valid {
		return saved, nil
	}
	// Without a matching durable parser position, no rows from an interrupted
	// build may be recognized as a validated prefix of the source.
	_, err = db.ExecContext(ctx, `DELETE FROM event_locations; DELETE FROM event_pending; DELETE FROM entries; DELETE FROM metadata`)
	return p, err
}

func validateEventPagerFile(source string, file *os.File, fingerprint string) error {
	key, err := displayImportFileKey(store.SessionEventLog(source), file)
	if err != nil {
		return err
	}
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	target, version := fileops.DiskSnapshot(source, info)
	if fmt.Sprintf("%s:%s:event:%s:schema1", target.Key, version, key) != fingerprint {
		return ErrDisplaySourceChanged
	}
	return nil
}

func restoreEventProjection(ctx context.Context, db *sql.DB, idx *SessionDisplayIndex, users *int, digest hash.Hash, count int) error {
	var raw string
	err := db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='event_projection_progress'`).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var saved checkpointPagerProgress
	valid := err == nil && json.Unmarshal([]byte(raw), &saved) == nil && saved.Header.SchemaVersion == SessionDisplayIndexSchemaVersion &&
		saved.Header.TranscriptSize == idx.TranscriptSize && saved.Header.Entries == nil && saved.Header.MessageCount >= 0 &&
		saved.Header.MessageCount <= count && saved.Users >= 0 && saved.Users <= saved.Header.MessageCount &&
		saved.Header.AuthoredTurns >= 0 && saved.Header.AuthoredTurns <= saved.Header.MessageCount
	var rows, first, last int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MIN(position),0),COALESCE(MAX(position),-1) FROM entries`).Scan(&rows, &first, &last); err != nil {
		return err
	}
	valid = valid && rows == saved.Header.MessageCount && first == 0 && last == rows-1
	if valid && digest.(encoding.BinaryUnmarshaler).UnmarshalBinary(saved.Hash) == nil {
		*idx, *users = saved.Header, saved.Users
		return nil
	}
	digest.Reset()
	_, err = db.ExecContext(ctx, `DELETE FROM entries; DELETE FROM metadata WHERE key='event_projection_progress'`)
	return err
}

func saveEventProjection(ctx context.Context, tx *sql.Tx, idx SessionDisplayIndex, users int, digest hash.Hash) error {
	state, err := digest.(encoding.BinaryMarshaler).MarshalBinary()
	if err != nil {
		return err
	}
	body, err := json.Marshal(checkpointPagerProgress{Header: idx, Users: users, Hash: state})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO metadata VALUES('event_projection_progress',?)`, string(body))
	return err
}

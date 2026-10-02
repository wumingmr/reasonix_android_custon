package agent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"reasonix/internal/fileops"
	"reasonix/internal/provider"
	"reasonix/internal/store"
)

// Only display-relevant head state is retained. Message bodies and execution
// state (writers, open turns, compaction) never enter this disposable cache.
type dagPagerHead struct {
	ID, Leaf, System        string
	CreatedAt, LastActivity time.Time
	LastOffset              int64
	Retired                 bool
}

type dagPagerProgress struct {
	Version          int
	SourceKey, Phase string
	Offset           int64
	Records          int
	Heads            []dagPagerHead `json:"-"`
	HeadPositions    map[string]int `json:"-"`
	HeadCount        int
	Selected         string
	HeadID, NextID   string
	ChainCount       int
	NextPosition     int
	Header           SessionDisplayIndex
	Users            int
	Hash             []byte
}

func (p *dagPagerProgress) snapshot(st *sessionDAGState) {
	p.Selected = st.selected
	p.Heads = make([]dagPagerHead, 0, len(st.headOrder))
	for _, id := range st.headOrder {
		h := st.heads[id]
		p.Heads = append(p.Heads, snapshotDAGPagerHead(h))
	}
	p.HeadCount = len(p.Heads)
}

func snapshotDAGPagerHead(h *sessionDAGHead) dagPagerHead {
	saved := dagPagerHead{ID: h.id, Leaf: h.leaf, CreatedAt: h.createdAt, LastActivity: h.lastActivity, LastOffset: h.lastOffset, Retired: h.retired}
	if h.system != nil {
		saved.System = h.system.ID
	}
	return saved
}

func (p *dagPagerProgress) state(source string) *sessionDAGState {
	st := newSessionDAGState(source)
	st.heads, st.headOrder, st.selected = map[string]*sessionDAGHead{}, nil, p.Selected
	for _, saved := range p.Heads {
		h := &sessionDAGHead{id: saved.ID, leaf: saved.Leaf, createdAt: saved.CreatedAt, lastActivity: saved.LastActivity, lastOffset: saved.LastOffset, retired: saved.Retired}
		if saved.System != "" {
			h.system = &provider.Message{ID: saved.System}
		}
		st.heads[h.id] = h
		st.headOrder = append(st.headOrder, h.id)
	}
	return st
}

func validateDAGPagerFile(source string, file *os.File, fingerprint, head string) error {
	key, err := displayImportFileKey(store.SessionEventLog(source), file)
	if err != nil {
		return err
	}
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	target, version := fileops.DiskSnapshot(source, info)
	info, err = file.Stat()
	if err != nil {
		return err
	}
	if fmt.Sprintf("%s:%s:event:%s:dag:%d:%d:%s", target.Key, version, key, info.Size(), info.ModTime().UnixNano(), head) != fingerprint {
		return ErrDisplaySourceChanged
	}
	return nil
}

func restoreDAGPagerProgress(ctx context.Context, db *sql.DB, source, fingerprint string, size int64) (*dagPagerProgress, error) {
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS dag_nodes(id TEXT PRIMARY KEY,parent TEXT NOT NULL,location BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS dag_overlays(kind TEXT NOT NULL,id TEXT NOT NULL,location BLOB NOT NULL,PRIMARY KEY(kind,id));
CREATE TABLE IF NOT EXISTS dag_chain(position INTEGER PRIMARY KEY,id TEXT UNIQUE NOT NULL);
CREATE TABLE IF NOT EXISTS dag_locations(position INTEGER PRIMARY KEY,location BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS dag_heads(position INTEGER PRIMARY KEY,id TEXT UNIQUE NOT NULL,record BLOB NOT NULL);`)
	if err != nil {
		return nil, err
	}
	var raw string
	err = db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='dag_progress'`).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var p dagPagerProgress
	valid := err == nil && json.Unmarshal([]byte(raw), &p) == nil && validDAGPagerHeader(&p, fingerprint, size)
	info, err := os.Stat(store.SessionEventLog(source))
	if err != nil {
		return nil, err
	}
	headsValid, err := restoreDAGPagerHeads(ctx, db, &p)
	if err != nil {
		return nil, err
	}
	valid = valid && headsValid && p.Offset <= info.Size() && validDAGPagerPhase(&p)
	for _, table := range []struct {
		name string
		rows int
	}{{"dag_chain", p.ChainCount}, {"entries", p.Header.MessageCount}, {"dag_locations", p.Header.MessageCount}} {
		var count, first, last int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MIN(position),0),COALESCE(MAX(position),-1) FROM `+table.name).Scan(&count, &first, &last); err != nil {
			return nil, err
		}
		valid = valid && count == table.rows && first == 0 && last == count-1
	}
	if valid {
		return &p, nil
	}
	// A parser position and its derived rows are an atomic unit. Never adopt
	// leftover rows when their durable progress proof is missing or invalid.
	_, err = db.ExecContext(ctx, `DELETE FROM dag_nodes; DELETE FROM dag_overlays; DELETE FROM dag_chain;
DELETE FROM dag_locations; DELETE FROM dag_heads; DELETE FROM entries; DELETE FROM metadata`)
	p = dagPagerProgress{Version: 1, SourceKey: fingerprint, Phase: "scan",
		Header: SessionDisplayIndex{SchemaVersion: SessionDisplayIndexSchemaVersion, TranscriptSize: size, ListingPreviewKnown: true}}
	p.snapshot(newSessionDAGState(source))
	return &p, err
}

func restoreDAGPagerHeads(ctx context.Context, db *sql.DB, p *dagPagerProgress) (bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT position,id,record FROM dag_heads ORDER BY position`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	valid := true
	for rows.Next() {
		var position int
		var id string
		var raw []byte
		var h dagPagerHead
		if err := rows.Scan(&position, &id, &raw); err != nil {
			return false, err
		}
		if json.Unmarshal(raw, &h) != nil || position != len(p.Heads) || id != h.ID {
			valid = false
		}
		p.Heads = append(p.Heads, h)
	}
	return valid && len(p.Heads) == p.HeadCount, rows.Err()
}

func validDAGPagerHeader(p *dagPagerProgress, fingerprint string, size int64) bool {
	return p.Version == 1 && p.SourceKey == fingerprint && p.Offset >= 0 && p.Records >= 0 && p.ChainCount >= 0 &&
		p.Header.SchemaVersion == SessionDisplayIndexSchemaVersion && p.Header.TranscriptSize == size && p.Header.Entries == nil &&
		p.Header.MessageCount >= 0 && p.Users >= 0 && p.Users <= p.Header.MessageCount && p.Header.AuthoredTurns >= 0 && p.Header.AuthoredTurns <= p.Header.MessageCount
}

func validDAGPagerPhase(p *dagPagerProgress) bool {
	seen := map[string]bool{}
	for _, h := range p.Heads {
		if h.ID == "" || seen[h.ID] || h.LastOffset < 0 || h.LastOffset > p.Offset {
			return false
		}
		seen[h.ID] = true
	}
	if !seen[SessionMainHead] {
		return false
	}
	switch p.Phase {
	case "scan":
		return p.ChainCount == 0 && p.Header.MessageCount == 0
	case "chain":
		return seen[p.HeadID] && p.Header.MessageCount == 0
	case "projection", "done":
		if !seen[p.HeadID] || p.NextID != "" || p.NextPosition < -1 || p.NextPosition >= p.ChainCount {
			return false
		}
		processed := p.ChainCount - p.NextPosition - 1
		if p.Header.MessageCount < processed || p.Header.MessageCount > processed+1 {
			return false
		}
		hash := sha256.New()
		if hash.(encoding.BinaryUnmarshaler).UnmarshalBinary(p.Hash) != nil {
			return false
		}
		return p.Phase != "done" || p.NextPosition == -1 && p.Header.ContentDigest == fmt.Sprintf("%x", hash.Sum(nil))
	default:
		return false
	}
}

func commitDAGPagerProgress(ctx context.Context, tx *sql.Tx, p *dagPagerProgress) error {
	body, err := json.Marshal(p)
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO metadata VALUES('dag_progress',?)`, string(body))
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

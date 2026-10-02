package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"time"

	"reasonix/internal/fileops"
	"reasonix/internal/historywork"
	"reasonix/internal/provider"
	"reasonix/internal/store"
)

// DAG projections retain graph edges and source locations on disk. Bodies are
// decoded one entry at a time, never accumulated in the runtime message array.
// The authoritative graph and selected head are never modified by this reader.
type displayDAGLocation struct {
	Offset int64     `json:"offset"`
	Length int64     `json:"length"`
	ID     string    `json:"id"`
	Target string    `json:"target,omitempty"`
	At     time.Time `json:"at"`
}

func readDisplayDAGMessage(ctx context.Context, file *os.File, loc displayDAGLocation) (provider.Message, error) {
	var entry sessionDAGEntry
	reader := &historywork.Reader{Context: ctx, Source: io.NewSectionReader(file, loc.Offset, loc.Length)}
	if err := json.NewDecoder(reader).Decode(&entry); err != nil {
		return provider.Message{}, err
	}
	raw := entry.Msgs
	if loc.Target != "" {
		raw = entry.Targets[loc.Target]
	}
	var messages []provider.Message
	if err := json.Unmarshal(raw, &messages); err != nil {
		return provider.Message{}, err
	}
	if len(messages) != 1 {
		return provider.Message{}, ErrSessionDisplayReadModelDamaged
	}
	m := messages[0]
	if loc.ID != "" {
		m.ID = loc.ID
	}
	return m, ctx.Err()
}

func buildDAGDisplayPager(ctx context.Context, db *sql.DB, source, fingerprint, requestedHead string, checkpointSize int64) error {
	return buildDAGDisplayPagerObserved(ctx, db, source, fingerprint, requestedHead, checkpointSize, nil)
}

func buildDAGDisplayPagerObserved(ctx context.Context, db *sql.DB, source, fingerprint, requestedHead string, checkpointSize int64, observed func(string, int)) error {
	f, err := fileops.OpenReplaceableRead(store.SessionEventLog(source))
	if err != nil {
		return err
	}
	defer f.Close()
	if err := validateDAGPagerFile(source, f, fingerprint, requestedHead); err != nil {
		return err
	}
	p, err := restoreDAGPagerProgress(ctx, db, source, fingerprint, checkpointSize)
	if err != nil {
		return err
	}
	idx, headID, err := resumeDAGDisplayPager(ctx, db, f, p, source, requestedHead, observed)
	if err != nil {
		return err
	}
	identity, known, err := SessionContentIdentity(source)
	if err != nil {
		return err
	}
	if known && requestedHead == "" {
		if idx.ContentDigest != identity.DigestHex {
			return fmt.Errorf("DAG selected history identity changed")
		}
		idx.Revision, idx.RevisionKnown = identity.Revision, identity.RevisionKnown
	}
	body, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	if _, err = db.ExecContext(ctx, `INSERT OR REPLACE INTO metadata VALUES('source',?),('header',?),('kind','dag'),('head',?)`, fingerprint, body, headID); err != nil {
		return err
	}
	return validateDAGPagerFile(source, f, fingerprint, requestedHead)
}

// Both ordinary reads and private rebuild transactions use this interface.
type displayDAGQueries interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func storeDisplayDAGOverlay(ctx context.Context, db displayDAGQueries, f *os.File, kind, id string, loc displayDAGLocation) error {
	if _, err := readDisplayDAGMessage(ctx, f, loc); err != nil {
		return err
	}
	body, err := json.Marshal(loc)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT OR REPLACE INTO dag_overlays VALUES(?,?,?)`, kind, id, body)
	return err
}

func displayDAGSystem(ctx context.Context, db displayDAGQueries, key string) (displayDAGLocation, error) {
	var loc displayDAGLocation
	var raw []byte
	if err := db.QueryRowContext(ctx, `SELECT location FROM dag_overlays WHERE kind='system' AND id=?`, key).Scan(&raw); err != nil {
		return loc, err
	}
	err := json.Unmarshal(raw, &loc)
	return loc, err
}

func (p *DisplayPager) DAGMessages(lo, hi int) ([]provider.Message, error) {
	if lo < 0 || hi < lo || hi-lo > 500 {
		return nil, fmt.Errorf("display window exceeds page budget")
	}
	rows, err := p.DB.QueryContext(p.ctx, `SELECT location FROM dag_locations WHERE position>=? AND position<? ORDER BY position`, lo, hi)
	if err != nil {
		return nil, err
	}
	var locations []displayDAGLocation
	for rows.Next() {
		var body []byte
		var loc displayDAGLocation
		if err = rows.Scan(&body); err == nil {
			err = json.Unmarshal(body, &loc)
		}
		if err != nil {
			break
		}
		locations = append(locations, loc)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return nil, err
	}
	f, err := fileops.OpenReplaceableRead(store.SessionEventLog(p.source))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	result := make([]provider.Message, 0, len(locations))
	for _, loc := range locations {
		m, err := readDisplayDAGMessage(p.ctx, f, loc)
		if err != nil {
			return nil, err
		}
		if m.CreatedAt <= 0 && !loc.At.IsZero() {
			m.CreatedAt = loc.At.UnixMilli()
		}
		result = append(result, m)
	}
	return result, p.Validate()
}

func applyDisplayDAGLocation(ctx context.Context, db displayDAGQueries, f *os.File, state *sessionDAGState, e sessionDAGEntry, start, end int64) error {
	loc := displayDAGLocation{Offset: start, Length: end - start, ID: e.ID, At: e.At}
	switch e.Type {
	case sessionDAGTypeMessage:
		if e.ID == "" {
			return ErrSessionDisplayReadModelDamaged
		}
		// Validate each record, including branches outside the selected view.
		if _, err := readDisplayDAGMessage(ctx, f, loc); err != nil {
			return err
		}
		encoded, err := json.Marshal(loc)
		if err != nil {
			return err
		}
		res, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO dag_nodes VALUES(?,?,?)`, e.ID, e.Parent, encoded)
		if err != nil {
			return err
		}
		if count, _ := res.RowsAffected(); count == 0 {
			return nil
		}
		h := state.headFor(e.Head, e.At)
		h.leaf, h.lastActivity, h.lastOffset = e.ID, e.At, end
	case sessionDAGTypePatch, sessionDAGTypeSystem, sessionDAGTypeRedact:
		if e.Type == sessionDAGTypePatch {
			var exists int
			if err := db.QueryRowContext(ctx, `SELECT 1 FROM dag_nodes WHERE id=?`, e.Target).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
				return nil
			} else if err != nil {
				return err
			}
			loc.ID = e.Target
		}
		if e.Type == sessionDAGTypeSystem {
			// Fork copies this immutable locator key, matching the native
			// replay's inherited system override without retaining its body.
			loc.ID = ""
			key := fmt.Sprint(start)
			state.headFor(e.Head, e.At).system = &provider.Message{ID: key}
			if err := storeDisplayDAGOverlay(ctx, db, f, "system", key, loc); err != nil {
				return err
			}
		} else if e.Type == sessionDAGTypeRedact {
			for id := range e.Targets {
				loc.ID, loc.Target = id, id
				if err := storeDisplayDAGOverlay(ctx, db, f, "redact", id, loc); err != nil {
					return err
				}
			}
		} else if err := storeDisplayDAGOverlay(ctx, db, f, "patch", e.Target, loc); err != nil {
			return err
		}
	case sessionDAGTypeFork, sessionDAGTypeRewind, sessionDAGTypeSelect, sessionDAGTypeRename, sessionDAGTypeRetire,
		sessionDAGTypeTurnBegin, sessionDAGTypeTurnEnd, sessionDAGTypeCompaction:
		if !state.applyHeadMarker(e, end) {
			return ErrSessionDisplayReadModelDamaged
		}
	case sessionDAGTypeLog:
		state.generation = e.Generation
	case sessionDAGTypeWriter, sessionDAGTypeCheckpoint:
	default:
		return fmt.Errorf("unsupported DAG entry type %q", e.Type)
	}
	return nil
}

type displayDAGProjectionWriter struct {
	ctx    context.Context
	db     displayDAGQueries
	file   *os.File
	index  SessionDisplayIndex
	hasher hash.Hash
	users  int
}

func (w *displayDAGProjectionWriter) append(loc displayDAGLocation) error {
	ctx, db, f := w.ctx, w.db, w.file
	idx, hasher := &w.index, w.hasher
	m, err := readDisplayDAGMessage(ctx, f, loc)
	if err != nil {
		return err
	}
	entry, turn := classifyDisplayIndexMessage(m, idx.MessageCount, loc.Offset, loc.Length, idx.AuthoredTurns)
	idx.AuthoredTurns = turn
	entryJSON, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	locationJSON, err := json.Marshal(loc)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO entries VALUES(?,?,?,?,?,?,?)`, entry.Index, entry.Offset, entry.Length, entry.AuthoredTurn, entry.Role, w.users, entryJSON); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO dag_locations VALUES(?,?)`, entry.Index, locationJSON); err != nil {
		return err
	}
	body, err := json.Marshal(messageForSessionIdentity(m))
	if err != nil {
		return err
	}
	hasher.Write(body)
	hasher.Write([]byte{'\n'})
	if m.Role == provider.RoleUser && !IsPinnedContextRevision(m) {
		w.users++
	}
	if entry.StartsTurn && idx.ListingPreview == "" {
		idx.ListingPreview = truncatePreview(previewProse(UserMessageText(m)))
	}
	idx.MessageCount++
	return nil
}

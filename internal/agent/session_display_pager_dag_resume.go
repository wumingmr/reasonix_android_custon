package agent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"reasonix/internal/historywork"
	"reasonix/internal/provider"
)

func dagPagerBatch(ctx context.Context, db *sql.DB, p *dagPagerProgress, work func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := work(tx); err != nil {
		return err
	}
	return commitDAGPagerProgress(ctx, tx, p)
}

func resumeDAGDisplayPager(ctx context.Context, db *sql.DB, f *os.File, p *dagPagerProgress, source, requestedHead string, observed func(string, int)) (SessionDisplayIndex, string, error) {
	st := p.state(source)
	p.HeadPositions = make(map[string]int, len(st.headOrder))
	for position, id := range st.headOrder {
		p.HeadPositions[id] = position
	}
	p.Heads = nil
	base := p.Offset
	decoder := json.NewDecoder(&historywork.Reader{Context: ctx, Source: io.NewSectionReader(f, base, 1<<63-1-base)})
	for p.Phase != "done" {
		if err := ctx.Err(); err != nil {
			return SessionDisplayIndex{}, "", err
		}
		phase := p.Phase
		err := dagPagerBatch(ctx, db, p, func(tx *sql.Tx) error {
			switch phase {
			case "scan":
				return scanDAGPagerBatch(ctx, tx, f, decoder, base, p, st, requestedHead)
			case "chain":
				return chainDAGPagerBatch(ctx, tx, p)
			case "projection":
				return projectDAGPagerBatch(ctx, tx, f, p, st.heads[p.HeadID])
			default:
				return ErrSessionDisplayReadModelDamaged
			}
		})
		if err != nil {
			return SessionDisplayIndex{}, "", err
		}
		if observed != nil {
			count := p.Records
			switch phase {
			case "chain":
				count = p.ChainCount
			case "projection":
				count = p.Header.MessageCount
			}
			observed(phase, count)
		}
	}
	return p.Header, p.HeadID, ctx.Err()
}

func scanDAGPagerBatch(ctx context.Context, tx *sql.Tx, f *os.File, decoder *json.Decoder, base int64, p *dagPagerProgress, st *sessionDAGState, requestedHead string) error {
	started, from := time.Now(), p.Offset
	changed := map[string]bool{SessionMainHead: true}
	for range historywork.BatchEntries {
		if err := ctx.Err(); err != nil {
			return err
		}
		start := base + decoder.InputOffset()
		var e sessionDAGEntry
		if err := decoder.Decode(&e); errors.Is(err, io.EOF) {
			p.Phase, p.HeadID = "chain", requestedHead
			if p.HeadID == "" {
				p.HeadID = st.selectedHead()
			}
			head := st.heads[p.HeadID]
			if head == nil {
				return fmt.Errorf("history head not found")
			}
			p.NextID = head.leaf
			break
		} else if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("DAG history: %w", ErrSessionDisplayReadModelDamaged)
		}
		if e.SchemaVersion != sessionDAGSchemaVersion {
			return fmt.Errorf("unsupported DAG schema %d", e.SchemaVersion)
		}
		p.Offset = base + decoder.InputOffset()
		if err := applyDisplayDAGLocation(ctx, tx, f, st, e, start, p.Offset); err != nil {
			return err
		}
		head := e.Head
		if head == "" {
			head = SessionMainHead
		}
		changed[head], changed[e.NewHead] = true, true
		for p.HeadCount < len(st.headOrder) {
			id := st.headOrder[p.HeadCount]
			p.HeadPositions[id] = p.HeadCount
			changed[id] = true
			p.HeadCount++
		}
		p.Records++
		if p.Offset-from >= historywork.BatchBytes || time.Since(started) >= historywork.SliceDuration {
			break
		}
	}
	p.Selected = st.selected
	// Persist only touched heads. Rewriting every head at every parser checkpoint
	// would turn a large fork log into quadratic metadata I/O.
	for id := range changed {
		if h := st.heads[id]; h != nil {
			body, err := json.Marshal(snapshotDAGPagerHead(h))
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO dag_heads VALUES(?,?,?)`, p.HeadPositions[id], id, body); err != nil {
				return err
			}
		}
	}
	return nil
}

func chainDAGPagerBatch(ctx context.Context, tx *sql.Tx, p *dagPagerProgress) error {
	started := time.Now()
	for count := 0; p.NextID != "" && count < historywork.BatchEntries; count++ {
		var parent string
		if err := tx.QueryRowContext(ctx, `SELECT parent FROM dag_nodes WHERE id=?`, p.NextID).Scan(&parent); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("DAG history missing ancestor: %w", ErrSessionDisplayReadModelDamaged)
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO dag_chain VALUES(?,?)`, p.ChainCount, p.NextID); err != nil {
			return fmt.Errorf("DAG history cycle or chain write failure: %w", err)
		}
		p.ChainCount++
		p.NextID = parent
		if time.Since(started) >= historywork.SliceDuration {
			break
		}
	}
	if p.NextID == "" {
		p.Phase, p.NextPosition = "projection", p.ChainCount-1
		var err error
		p.Hash, err = sha256.New().(encoding.BinaryMarshaler).MarshalBinary()
		return err
	}
	return nil
}

func projectDAGPagerBatch(ctx context.Context, tx *sql.Tx, f *os.File, p *dagPagerProgress, head *sessionDAGHead) error {
	w := &displayDAGProjectionWriter{ctx: ctx, db: tx, file: f, index: p.Header, users: p.Users, hasher: sha256.New()}
	if err := w.hasher.(encoding.BinaryUnmarshaler).UnmarshalBinary(p.Hash); err != nil {
		return err
	}
	started := time.Now()
	for count := 0; p.NextPosition >= 0 && count < historywork.BatchEntries; count++ {
		if err := appendDAGPagerPosition(ctx, tx, p, head, w); err != nil {
			return err
		}
		p.NextPosition--
		if time.Since(started) >= historywork.SliceDuration {
			break
		}
	}
	if p.ChainCount == 0 && head.system != nil {
		loc, err := displayDAGSystem(ctx, tx, head.system.ID)
		if err != nil {
			return err
		}
		if err := w.append(loc); err != nil {
			return err
		}
	}
	p.Header, p.Users = w.index, w.users
	var err error
	p.Hash, err = w.hasher.(encoding.BinaryMarshaler).MarshalBinary()
	if p.NextPosition < 0 {
		p.Phase = "done"
		p.Header.ContentDigest = fmt.Sprintf("%x", w.hasher.Sum(nil))
	}
	return err
}

func appendDAGPagerPosition(ctx context.Context, tx *sql.Tx, p *dagPagerProgress, head *sessionDAGHead, w *displayDAGProjectionWriter) error {
	var id string
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT n.id,COALESCE(r.location,p.location,n.location) FROM dag_chain c JOIN dag_nodes n ON n.id=c.id
LEFT JOIN dag_overlays p ON p.kind='patch' AND p.id=n.id LEFT JOIN dag_overlays r ON r.kind='redact' AND r.id=n.id WHERE c.position=?`, p.NextPosition).Scan(&id, &raw); err != nil {
		return err
	}
	var loc displayDAGLocation
	if err := json.Unmarshal(raw, &loc); err != nil {
		return err
	}
	loc.ID = id
	if p.NextPosition == p.ChainCount-1 && head.system != nil {
		m, err := readDisplayDAGMessage(ctx, w.file, loc)
		if err != nil {
			return err
		}
		sys, err := displayDAGSystem(ctx, tx, head.system.ID)
		if err != nil {
			return err
		}
		if m.Role == provider.RoleSystem {
			sys.ID = id
			loc = sys
		} else if err := w.append(sys); err != nil {
			return err
		}
	}
	return w.append(loc)
}

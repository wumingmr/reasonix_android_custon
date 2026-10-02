package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"reasonix/internal/fileops"
	"reasonix/internal/historywork"
	"reasonix/internal/projectiondb"
	"reasonix/internal/provider"
	"reasonix/internal/store"
)

// DisplayPager is a disposable disk projection of exact legacy byte offsets.
// It never converts or rewrites the source transcript. Header contains no
// Entries; individual positions are queried through SQLite's bounded cache.
type DisplayPager struct {
	DB            *sql.DB
	Header        SessionDisplayIndex
	Built         bool
	DAG           bool
	SchemaOne     bool
	ctx           context.Context
	source        string
	sourceInfo    os.FileInfo
	eventInfo     os.FileInfo
	sourceVersion fileops.Version
	eventVersion  fileops.Version
}

var ErrDisplaySourceChanged = errors.New("display source changed")
var ErrDisplayFormatUnsupported = errors.New("display format requires compatibility reader")

var displayPagerMigrations = []projectiondb.Migration{{Version: 1, Apply: func(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE metadata(key TEXT PRIMARY KEY,value TEXT NOT NULL);
	CREATE TABLE entries(position INTEGER PRIMARY KEY,offset INTEGER NOT NULL,length INTEGER NOT NULL,turn INTEGER NOT NULL,role TEXT NOT NULL,user_before INTEGER NOT NULL,entry BLOB NOT NULL);
	CREATE INDEX entries_turn ON entries(turn,position);`)
	return err
}}}

func OpenDisplayPager(ctx context.Context, source, cachePath string, heads ...string) (*DisplayPager, error) {
	head := ""
	if len(heads) > 0 {
		head = heads[0]
	}
	return openDisplayPager(ctx, source, cachePath, head, false)
}

func openDisplayPager(ctx context.Context, source, cachePath, head string, forceSource bool) (*DisplayPager, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Stat(source)
	if err != nil {
		return nil, err
	}
	identity, known, err := SessionContentIdentity(source)
	if err != nil {
		return nil, err
	}
	indexPath := store.SessionDisplayIndex(source)
	indexInfo, err := os.Stat(indexPath)
	plain := os.IsNotExist(err)
	if err != nil && !plain {
		return nil, err
	}
	// Equal timestamps are ambiguous. Leave them to the explicit authoritative
	// preparation path, never certify stale offsets from size alone.
	if !plain && (forceSource || !known || !indexInfo.ModTime().After(SessionContentModTime(source))) {
		plain = true
	}
	sourceTarget, sourceVersion := fileops.DiskSnapshot(source, info)
	fingerprint := fmt.Sprintf("%s:%s", sourceTarget.Key, sourceVersion)
	opts := projectiondb.OpenOptions{Path: cachePath, Migrations: displayPagerMigrations, RequireDisk: true, MaxOpenConns: 1}
	handle, err := projectiondb.Open(ctx, opts)
	if err != nil {
		return nil, err
	}
	defer func() {
		if handle != nil {
			_ = handle.DB.Close()
		}
	}()
	var stored string
	storedErr := handle.DB.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='source'`).Scan(&stored)
	observed, err := observeDisplayPagerEvents(ctx, source, head, forceSource, plain, indexInfo, fingerprint, stored, storedErr)
	if err != nil {
		return nil, err
	}
	fingerprint, plain = observed.fingerprint, observed.plain
	eventInfo, eventVersion, dag, schemaOne := observed.eventInfo, observed.eventVersion, observed.dag, observed.schemaOne
	built := storedErr != nil || stored != fingerprint
	if built {
		_ = handle.DB.Close()
		err = observed.rebuild(ctx, opts, source, indexPath, head, info.Size())
		if err != nil {
			if !dag && !schemaOne && !plain && ctx.Err() == nil {
				return openDisplayPager(ctx, source, cachePath, head, true)
			}
			return nil, err
		}
		handle, err = projectiondb.Open(ctx, opts)
		if err != nil {
			return nil, err
		}
	}
	p := &DisplayPager{DB: handle.DB, ctx: ctx, source: source, sourceInfo: info, eventInfo: eventInfo, sourceVersion: sourceVersion, eventVersion: eventVersion, DAG: dag, SchemaOne: schemaOne, Built: built && (plain || dag || schemaOne)}
	matching, err := p.loadMatchingHeader(ctx, info, identity, known, head)
	if err != nil {
		p.Close()
		return nil, err
	}
	if !matching {
		p.Close()
		if !dag && !schemaOne && !plain {
			return openDisplayPager(ctx, source, cachePath, head, true)
		}
		return nil, ErrDisplaySourceChanged
	}

	if err := p.prepareQueries(ctx); err != nil {
		p.Close()
		return nil, err
	}
	handle = nil // The returned pager owns the open database from here.
	return p, nil
}

func (p *DisplayPager) prepareQueries(ctx context.Context) error {
	if err := p.Validate(); err != nil {
		return err
	}
	// Optional accelerator over disposable metadata. Existing cache readers
	// can ignore it; no authoritative format or schema version changes.
	_, err := p.DB.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS entries_authored_turn ON entries(turn,position) WHERE json_extract(CAST(entry AS TEXT),'$.starts_turn')=1`)
	return err
}

func (p *DisplayPager) Close() error { return p.DB.Close() }

// WithContext borrows the projection for a single caller. Only its preparation
// owner closes the database; canceling a read cannot cancel other readers.
func (p *DisplayPager) WithContext(ctx context.Context) *DisplayPager {
	copy := *p
	copy.ctx = ctx
	return &copy
}

func (p *DisplayPager) Validate() error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	st, err := os.Stat(p.source)
	if err != nil {
		return err
	}
	if !os.SameFile(st, p.sourceInfo) || st.Size() != p.sourceInfo.Size() || !st.ModTime().Equal(p.sourceInfo.ModTime()) {
		return ErrDisplaySourceChanged
	}
	if _, version := fileops.DiskSnapshot(p.source, st); version != p.sourceVersion {
		return ErrDisplaySourceChanged
	}
	if p.eventInfo != nil {
		current, err := os.Stat(store.SessionEventLog(p.source))
		if err != nil {
			return err
		}
		if !os.SameFile(current, p.eventInfo) || current.Size() != p.eventInfo.Size() || !current.ModTime().Equal(p.eventInfo.ModTime()) {
			return ErrDisplaySourceChanged
		}
		if _, version := fileops.DiskSnapshot(store.SessionEventLog(p.source), current); version != p.eventVersion {
			return ErrDisplaySourceChanged
		}
	} else {
		current, err := os.Stat(store.SessionEventLog(p.source))
		if err == nil && current.Size() > 0 {
			return ErrDisplaySourceChanged
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (p *DisplayPager) Entry(position int) (DisplayIndexEntry, error) {
	var entry DisplayIndexEntry
	var body []byte
	if err := p.DB.QueryRowContext(p.ctx, `SELECT entry FROM entries WHERE position=?`, position).Scan(&body); err != nil {
		return entry, err
	}
	err := json.Unmarshal(body, &entry)
	return entry, err
}

func (p *DisplayPager) Entries(lo, hi int) ([]DisplayIndexEntry, error) {
	if lo < 0 || hi < lo || hi-lo > 500 {
		return nil, errors.New("display window exceeds page budget")
	}
	rows, err := p.DB.QueryContext(p.ctx, `SELECT entry FROM entries WHERE position>=? AND position<? ORDER BY position`, lo, hi)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]DisplayIndexEntry, 0, hi-lo)
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		var e DisplayIndexEntry
		if err := json.Unmarshal(b, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (p *DisplayPager) UsersBefore(position int) (int, error) {
	var count int
	err := p.DB.QueryRowContext(p.ctx, `SELECT user_before FROM entries WHERE position=?`, position).Scan(&count)
	return count, err
}

// TurnEntries returns only authored user positions in the selected branch.
// Hidden control messages and long tool runs do not require prefix decoding.
func (p *DisplayPager) TurnEntries(start, limit int) ([]DisplayIndexEntry, error) {
	if limit <= 0 || limit > 1000 {
		return nil, errors.New("display outline exceeds page budget")
	}
	rows, err := p.DB.QueryContext(p.ctx, `SELECT entry FROM entries WHERE turn>=? AND json_extract(CAST(entry AS TEXT),'$.starts_turn')=1 ORDER BY turn,position LIMIT ?`, max(start, 1), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]DisplayIndexEntry, 0, limit)
	for rows.Next() {
		var body []byte
		var entry DisplayIndexEntry
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// Import the existing JSON index one entry at a time. A corrupt or cancelled
// build never replaces the last published projection.
func importDisplayPager(ctx context.Context, db *sql.DB, indexPath, fingerprint string) error {
	return importDisplayPagerObserved(ctx, db, indexPath, fingerprint, nil)
}

func importDisplayPagerObserved(ctx context.Context, db *sql.DB, indexPath, fingerprint string, committed func(int)) error {
	f, err := os.Open(indexPath)
	if err != nil {
		return err
	}
	defer f.Close()
	key, err := displayImportFileKey(indexPath, f)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	progress, err := restoreDisplayImportProgress(ctx, db, key, info.Size())
	if err != nil {
		return err
	}
	decoder, base, err := displayImportDecoder(ctx, f, progress.sourceOffset)
	if err != nil {
		return err
	}
	progress.decoderBase, progress.committed = base, committed
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("invalid display index object")
	}
	header := progress.header
	sawEntries := false
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := key.(string)
		if !ok {
			return errors.New("invalid display index field")
		}
		if name != "entries" {
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				return err
			}
			header[name] = raw
			continue
		}
		if sawEntries {
			return errors.New("duplicate display entries")
		}
		sawEntries = true
		token, err := decoder.Token()
		if err != nil || token != json.Delim('[') {
			return errors.New("invalid display entries")
		}
		if err := progress.readEntries(ctx, db, decoder); err != nil {
			return err
		}
		if _, err := decoder.Token(); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.Join(errors.New("trailing display index data"), err)
	}
	body, err := json.Marshal(header)
	if err != nil {
		return err
	}
	var idx SessionDisplayIndex
	if err := json.Unmarshal(body, &idx); err != nil {
		return err
	}
	if !sawEntries || idx.SchemaVersion != SessionDisplayIndexSchemaVersion || idx.MessageCount != progress.count || idx.TranscriptSize != progress.offset {
		return errors.New("display index header does not cover entries")
	}
	current, err := displayImportFileKey(indexPath, f)
	if err != nil {
		return err
	}
	if current != key {
		return ErrDisplaySourceChanged
	}
	_, err = db.ExecContext(ctx, `INSERT OR REPLACE INTO metadata VALUES('source',?),('header',?)`, fingerprint, string(body))
	return err
}

type displayIndexImportProgress struct {
	count, users int
	offset       int64
	sourceOffset int64
	decoderBase  int64
	sourceKey    string
	header       map[string]json.RawMessage
	committed    func(int)
}

func (progress *displayIndexImportProgress) readEntries(ctx context.Context, db *sql.DB, decoder *json.Decoder) error {
	for decoder.More() {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for batch := 0; batch < historywork.BatchEntries && decoder.More(); batch++ {
			var e DisplayIndexEntry
			if err = decoder.Decode(&e); err != nil {
				break
			}
			if e.Index != progress.count || e.Offset != progress.offset || e.Length <= 0 {
				err = errors.New("invalid display offset chain")
				break
			}
			body, marshalErr := json.Marshal(e)
			if marshalErr != nil {
				err = marshalErr
				break
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO entries VALUES(?,?,?,?,?,?,?)`, progress.count, e.Offset, e.Length, e.AuthoredTurn, e.Role, progress.users, body)
			if err != nil {
				break
			}
			if e.Role == provider.RoleUser && !e.PinnedContextRevision {
				progress.users++
			}
			progress.count++
			progress.offset += e.Length
		}
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		progress.sourceOffset = decoder.InputOffset() + progress.decoderBase
		if err := progress.save(ctx, tx); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		if progress.committed != nil {
			progress.committed(progress.count)
		}
	}
	return nil
}

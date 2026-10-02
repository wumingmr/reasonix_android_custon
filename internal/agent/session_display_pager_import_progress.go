package agent

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"reasonix/internal/fileops"
	"reasonix/internal/historywork"
)

type displayImportCheckpoint struct {
	Version      int                        `json:"version"`
	SourceKey    string                     `json:"sourceKey"`
	SourceOffset int64                      `json:"sourceOffset"`
	Count        int                        `json:"count"`
	Users        int                        `json:"users"`
	Offset       int64                      `json:"offset"`
	Header       map[string]json.RawMessage `json:"header"`
}

func (progress *displayIndexImportProgress) save(ctx context.Context, tx *sql.Tx) error {
	body, err := json.Marshal(displayImportCheckpoint{1, progress.sourceKey, progress.sourceOffset, progress.count, progress.users, progress.offset, progress.header})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO metadata VALUES('display_import_progress',?)`, string(body))
	return err
}

func restoreDisplayImportProgress(ctx context.Context, db *sql.DB, key string, size int64) (*displayIndexImportProgress, error) {
	p := &displayIndexImportProgress{sourceKey: key, header: map[string]json.RawMessage{}}
	var raw string
	err := db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='display_import_progress'`).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var saved displayImportCheckpoint
	valid := err == nil && json.Unmarshal([]byte(raw), &saved) == nil && saved.Version == 1 && saved.SourceKey == key &&
		saved.SourceOffset > 0 && saved.SourceOffset < size && saved.Count > 0 && saved.Offset > 0 && saved.Users >= 0 && saved.Users <= saved.Count && saved.Header != nil
	var count int
	var end int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(offset+length),0) FROM entries`).Scan(&count, &end); err != nil {
		return nil, err
	}
	if valid && count == saved.Count && end == saved.Offset {
		p.count, p.users, p.offset = saved.Count, saved.Users, saved.Offset
		p.sourceOffset, p.header = saved.SourceOffset, saved.Header
		return p, nil
	}
	// The unfinished generation is disposable. Missing or damaged progress
	// cannot certify the rows that happen to remain in the staging database.
	_, err = db.ExecContext(ctx, `DELETE FROM entries; DELETE FROM metadata`)
	return p, err
}

func displayImportFileKey(path string, f *os.File) (string, error) {
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	current, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	target, version := fileops.DiskHandleSnapshot(path, f, info)
	pathTarget, pathVersion := fileops.DiskSnapshot(path, current)
	if !os.SameFile(info, current) || target.Key != pathTarget.Key || version != pathVersion {
		return "", ErrDisplaySourceChanged
	}
	return fmt.Sprintf("%s:%s", target.Key, version), nil
}

// A checkpoint is immediately after a complete array element. Recreate only
// the JSON framing, retaining all preceding header fields from the same batch.
// Decoder offsets are translated back to actual file offsets for the next
// durable checkpoint; buffered read-ahead is never mistaken for consumption.
func displayImportDecoder(ctx context.Context, f *os.File, offset int64) (*json.Decoder, int64, error) {
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, 0, err
	}
	reader := bufio.NewReaderSize(&historywork.Reader{Context: ctx, Source: f}, historywork.ReadChunk)
	if offset == 0 {
		return json.NewDecoder(reader), 0, nil
	}
	comma := false
	for {
		prefix, err := reader.Peek(1)
		if err != nil {
			return nil, 0, err
		}
		switch prefix[0] {
		case ' ', '\t', '\r', '\n':
		case ',':
			if comma {
				return nil, 0, errors.New("duplicate display entry separator")
			}
			comma = true
		default:
			if !comma && prefix[0] != ']' || comma && prefix[0] != '{' {
				return nil, 0, errors.New("invalid resumed display entry separator")
			}
			const framing = `{"entries":[`
			return json.NewDecoder(io.MultiReader(strings.NewReader(framing), reader)), offset - int64(len(framing)), nil
		}
		_, _ = reader.ReadByte()
		offset++
	}
}

package agent

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"reasonix/internal/fileops"
	"reasonix/internal/historywork"
	"reasonix/internal/provider"
	"reasonix/internal/store"
)

// Schema-1 replace/append logs retain original message locations. A large
// replace array is decoded one message at a time; SQLite holds the live order.
// No migration, normalization, or repair writes are made to source files.
func buildEventDisplayPager(ctx context.Context, db *sql.DB, source, fingerprint string, checkpointSize int64) error {
	return buildEventDisplayPagerObserved(ctx, db, source, fingerprint, checkpointSize, nil)
}

func buildEventDisplayPagerObserved(ctx context.Context, db *sql.DB, source, fingerprint string, checkpointSize int64, observed func(string, int)) error {
	f, err := fileops.OpenReplaceableRead(store.SessionEventLog(source))
	if err != nil {
		return err
	}
	defer f.Close()
	if err := validateEventPagerFile(source, f, fingerprint); err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	progress, err := restoreEventPagerScan(ctx, db, fingerprint, info.Size())
	if err != nil {
		return err
	}
	scanner := &eventPagerScanner{ctx: ctx, db: db, progress: progress, observed: observed}
	if err := scanner.scan(f); err != nil {
		return err
	}
	if err := finishEventDisplayPager(ctx, db, f, source, fingerprint, checkpointSize, scanner.progress.Count, observed); err != nil {
		return err
	}
	return validateEventPagerFile(source, f, fingerprint)
}

// Unknown event fields are skipped token by token rather than buffering the
// entire field/record in json.RawMessage. json.Decoder still validates nesting.
func skipDisplayJSONValue(ctx context.Context, decoder *json.Decoder) error {
	depth := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
		if depth <= 0 {
			return nil
		}
	}
}

type displayEventLocation struct {
	Position       int
	Offset, Length int64
	At             int64
}

func eventDisplayLocations(ctx context.Context, db *sql.DB, lo, hi int) ([]displayEventLocation, error) {
	rows, err := db.QueryContext(ctx, `SELECT position,offset,length,at FROM event_locations WHERE position>=? AND position<? ORDER BY position`, lo, hi)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	locations := make([]displayEventLocation, 0, hi-lo)
	for rows.Next() {
		var loc displayEventLocation
		if err := rows.Scan(&loc.Position, &loc.Offset, &loc.Length, &loc.At); err != nil {
			return nil, err
		}
		locations = append(locations, loc)
	}
	return locations, rows.Err()
}

func readDisplayEventMessage(ctx context.Context, file *os.File, loc displayEventLocation) (provider.Message, error) {
	reader := bufio.NewReaderSize(&historywork.Reader{Context: ctx, Source: io.NewSectionReader(file, loc.Offset, loc.Length)}, historywork.ReadChunk)
	// InputOffset may precede the comma separating adjacent array elements.
	for {
		prefix, err := reader.Peek(1)
		if err != nil {
			return provider.Message{}, err
		}
		if prefix[0] != ',' && prefix[0] != ' ' && prefix[0] != '\n' && prefix[0] != '\r' && prefix[0] != '\t' {
			break
		}
		_, _ = reader.ReadByte()
	}
	var message provider.Message
	err := json.NewDecoder(reader).Decode(&message)
	return message, err
}

func finishEventDisplayPager(ctx context.Context, db *sql.DB, file *os.File, source, fingerprint string, checkpointSize int64, count int, observed func(string, int)) error {
	idx := SessionDisplayIndex{SchemaVersion: SessionDisplayIndexSchemaVersion, TranscriptSize: checkpointSize, ListingPreviewKnown: true}
	hash := sha256.New()
	users := 0
	if err := restoreEventProjection(ctx, db, &idx, &users, hash, count); err != nil {
		return err
	}
	for lo := idx.MessageCount; lo < count; lo += historywork.BatchEntries {
		locations, err := eventDisplayLocations(ctx, db, lo, min(lo+historywork.BatchEntries, count))
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, loc := range locations {
			message, readErr := readDisplayEventMessage(ctx, file, loc)
			if readErr != nil {
				err = readErr
				break
			}
			var entry DisplayIndexEntry
			entry, idx.AuthoredTurns = classifyDisplayIndexMessage(message, idx.MessageCount, loc.Offset, loc.Length, idx.AuthoredTurns)
			body, _ := json.Marshal(entry)
			if _, err = tx.ExecContext(ctx, `INSERT INTO entries VALUES(?,?,?,?,?,?,?)`, entry.Index, entry.Offset, entry.Length, entry.AuthoredTurn, entry.Role, users, body); err != nil {
				break
			}
			body, err = json.Marshal(messageForSessionIdentity(message))
			if err != nil {
				break
			}
			hash.Write(body)
			hash.Write([]byte{'\n'})
			if entry.Role == provider.RoleUser && !entry.PinnedContextRevision {
				users++
			}
			if entry.StartsTurn && idx.ListingPreview == "" {
				idx.ListingPreview = truncatePreview(previewProse(UserMessageText(message)))
			}
			idx.MessageCount++
		}
		if err == nil {
			err = saveEventProjection(ctx, tx, idx, users, hash)
		}
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		if observed != nil {
			observed("projection", idx.MessageCount)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	idx.ContentDigest = fmt.Sprintf("%x", hash.Sum(nil))
	identity, known, err := SessionContentIdentity(source)
	if err != nil {
		return err
	}
	if known {
		if idx.ContentDigest != identity.DigestHex {
			return ErrDisplaySourceChanged
		}
		idx.Revision, idx.RevisionKnown = identity.Revision, identity.RevisionKnown
	}
	body, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT OR REPLACE INTO metadata VALUES('source',?),('header',?),('kind','schema1'); DROP TABLE IF EXISTS event_pending`, fingerprint, string(body))
	return err
}

// EventMessages reads a bounded range from either native event-log format.
func (p *DisplayPager) EventMessages(lo, hi int) ([]provider.Message, error) {
	if p.DAG {
		return p.DAGMessages(lo, hi)
	}
	if !p.SchemaOne || lo < 0 || hi < lo || hi-lo > 500 {
		return nil, errors.New("invalid event display window")
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	locations, err := eventDisplayLocations(p.ctx, p.DB, lo, hi)
	if err != nil {
		return nil, err
	}
	file, err := fileops.OpenReplaceableRead(store.SessionEventLog(p.source))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	messages := make([]provider.Message, 0, len(locations))
	for _, loc := range locations {
		message, err := readDisplayEventMessage(p.ctx, file, loc)
		if err != nil {
			return nil, err
		}
		if message.CreatedAt <= 0 && loc.At != 0 {
			message.CreatedAt = loc.At
		}
		messages = append(messages, message)
	}
	return messages, p.Validate()
}

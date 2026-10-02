package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"reasonix/internal/historywork"
	"reasonix/internal/provider"
)

// Each committed point is either a complete record or a complete element of
// its messages array. The staged rows and parser state commit together, even
// when a legacy writer puts type/schema fields after the messages array.
type eventPagerScanProgress struct {
	Version    int                `json:"version"`
	SourceKey  string             `json:"sourceKey"`
	Offset     int64              `json:"offset"`
	Count      int                `json:"count"`
	Pending    int                `json:"pending"`
	InMessages bool               `json:"inMessages"`
	Done       bool               `json:"done"`
	Record     sessionEventRecord `json:"record"`
}

type eventPagerScanner struct {
	ctx      context.Context
	db       *sql.DB
	tx       *sql.Tx
	decoder  *json.Decoder
	base     int64
	progress eventPagerScanProgress
	resuming bool
	observed func(string, int)
	records  int
}

func (s *eventPagerScanner) commit(inMessages, done bool) error {
	s.progress.Offset = s.base + s.decoder.InputOffset()
	s.progress.InMessages, s.progress.Done = inMessages, done
	body, err := json.Marshal(s.progress)
	if err == nil {
		_, err = s.tx.ExecContext(s.ctx, `INSERT OR REPLACE INTO metadata VALUES('event_scan_progress',?)`, string(body))
	}
	if err != nil {
		return err
	}
	if err := s.tx.Commit(); err != nil {
		return err
	}
	s.tx = nil
	s.records = 0
	if s.observed != nil {
		if inMessages {
			s.observed("messages", s.progress.Pending)
		} else {
			s.observed("records", s.progress.Count)
		}
	}
	return s.ctx.Err()
}

func (s *eventPagerScanner) transaction() error {
	if s.tx != nil {
		return nil
	}
	var err error
	s.tx, err = s.db.BeginTx(s.ctx, nil)
	return err
}

func (s *eventPagerScanner) messages() error {
	if !s.resuming {
		s.progress.Pending = 0
		if _, err := s.tx.ExecContext(s.ctx, `DELETE FROM event_pending`); err != nil {
			return err
		}
	}
	token, err := s.decoder.Token()
	if err != nil || token == nil {
		return err
	}
	if token != json.Delim('[') {
		return ErrSessionDisplayReadModelDamaged
	}
	if s.resuming {
		// Consume the synthetic prior element; the following separator and
		// remainder are validated by encoding/json with their original grammar.
		var previous any
		if err := s.decoder.Decode(&previous); err != nil {
			return err
		}
		s.resuming = false
	}
	for s.decoder.More() {
		if err := s.transaction(); err != nil {
			return err
		}
		start := s.base + s.decoder.InputOffset()
		var message provider.Message
		if err := s.decoder.Decode(&message); err != nil {
			return err
		}
		length := s.base + s.decoder.InputOffset() - start
		if _, err := s.tx.ExecContext(s.ctx, `INSERT INTO event_pending VALUES(?,?,?)`, s.progress.Pending, start, length); err != nil {
			return err
		}
		s.progress.Pending++
		if s.progress.Pending%historywork.BatchEntries == 0 {
			if err := s.commit(true, false); err != nil {
				return err
			}
		}
	}
	if _, err := s.decoder.Token(); err != nil {
		return err
	}
	return s.transaction()
}

func (s *eventPagerScanner) fields() error {
	for s.decoder.More() {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		field, err := s.decoder.Token()
		if err != nil {
			return err
		}
		switch field {
		case "schema_version":
			err = s.decoder.Decode(&s.progress.Record.SchemaVersion)
		case "type":
			err = s.decoder.Decode(&s.progress.Record.Type)
		case "message_index":
			err = s.decoder.Decode(&s.progress.Record.MessageIndex)
		case "created_at":
			err = s.decoder.Decode(&s.progress.Record.CreatedAt)
		case "messages":
			err = s.messages()
		default:
			err = skipDisplayJSONValue(s.ctx, s.decoder)
		}
		if err != nil {
			return err
		}
	}
	_, err := s.decoder.Token()
	return err
}

func (s *eventPagerScanner) applyRecord() error {
	record := s.progress.Record
	if record.SchemaVersion != sessionEventSchemaVersion {
		return ErrSessionDisplayReadModelDamaged
	}
	at := int64(0)
	switch record.Type {
	case sessionEventTypeReplace:
		if _, err := s.tx.ExecContext(s.ctx, `DELETE FROM event_locations`); err != nil {
			return err
		}
		s.progress.Count = 0
	case sessionEventTypeAppend:
		if record.MessageIndex != s.progress.Count {
			return ErrSessionDisplayReadModelDamaged
		}
		if !record.CreatedAt.IsZero() {
			at = record.CreatedAt.UnixMilli()
		}
	default:
		return ErrSessionDisplayReadModelDamaged
	}
	if _, err := s.tx.ExecContext(s.ctx, `INSERT INTO event_locations SELECT position+?,offset,length,? FROM event_pending`, s.progress.Count, at); err != nil {
		return err
	}
	s.progress.Count += s.progress.Pending
	s.progress.Pending, s.progress.Record = 0, sessionEventRecord{}
	_, err := s.tx.ExecContext(s.ctx, `DELETE FROM event_pending`)
	return err
}

func (s *eventPagerScanner) scan(file *os.File) (result error) {
	if s.progress.Done {
		return nil
	}
	if _, err := file.Seek(s.progress.Offset, io.SeekStart); err != nil {
		return err
	}
	var reader io.Reader = &historywork.Reader{Context: s.ctx, Source: file}
	s.base = s.progress.Offset
	s.resuming = s.progress.InMessages
	if s.resuming {
		const framing = `{"messages":[null`
		reader = io.MultiReader(strings.NewReader(framing), reader)
		s.base -= int64(len(framing))
	}
	s.decoder = json.NewDecoder(reader)
	defer func() {
		if s.tx != nil {
			_ = s.tx.Rollback()
		}
	}()
	for {
		if err := s.transaction(); err != nil {
			return err
		}
		token, err := s.decoder.Token()
		if errors.Is(err, io.EOF) {
			return s.commit(false, true)
		}
		if err != nil || token != json.Delim('{') {
			return errors.Join(ErrSessionDisplayReadModelDamaged, err)
		}
		if err := s.fields(); err != nil {
			return errors.Join(ErrSessionDisplayReadModelDamaged, err)
		}
		if err := s.applyRecord(); err != nil {
			return err
		}
		s.records++
		if s.records >= historywork.BatchEntries {
			if err := s.commit(false, false); err != nil {
				return err
			}
		}
	}
}

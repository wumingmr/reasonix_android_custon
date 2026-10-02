package agent

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"reasonix/internal/fileops"
	"reasonix/internal/historywork"
	"reasonix/internal/provider"
)

// Build an offset-only projection for a checkpoint without publishing or
// repairing any session sidecar. At most one message and one SQLite batch are
// retained. Digest validation happens before the disposable index is published.
func buildCheckpointDisplayPager(ctx context.Context, db *sql.DB, source, fingerprint string) error {
	return buildCheckpointDisplayPagerObserved(ctx, db, source, fingerprint, nil)
}

// The observer is used by deterministic interruption tests after durable batch
// publication. Production callers do not install one.
func buildCheckpointDisplayPagerObserved(ctx context.Context, db *sql.DB, source, fingerprint string, committed func(int)) error {
	f, err := fileops.OpenReplaceableRead(source)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := validateCheckpointPagerFile(source, f, fingerprint); err != nil {
		return err
	}
	hash := sha256.New()
	idx := SessionDisplayIndex{SchemaVersion: SessionDisplayIndexSchemaVersion, ListingPreviewKnown: true}
	users := 0
	if err := restoreCheckpointPagerProgress(ctx, db, &idx, &users, hash); err != nil {
		return err
	}
	if _, err := f.Seek(idx.TranscriptSize, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(&historywork.Reader{Context: ctx, Source: f}, historywork.ReadChunk)
	done := false
	for !done {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for range historywork.BatchEntries {
			line, readErr := readSessionDisplayIndexLine(reader)
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				err = readErr
				break
			}
			if len(line) > 0 {
				var message provider.Message
				if err = json.Unmarshal(line, &message); err != nil {
					break
				}
				if message.Role == "" {
					err = errors.New("checkpoint message has no role")
					// Ancient event rows use kind/type instead of provider roles.
					// Classify only the first record as an unsupported format;
					// a foreign record after valid messages is damaged content.
					if idx.MessageCount == 0 {
						var event struct {
							Kind string `json:"kind"`
							Type string `json:"type"`
						}
						if json.Unmarshal(line, &event) == nil && (event.Kind != "" || event.Type != "") {
							err = ErrDisplayFormatUnsupported
						}
					}
					break
				}
				var entry DisplayIndexEntry
				entry, idx.AuthoredTurns = classifyDisplayIndexMessage(message, idx.MessageCount, idx.TranscriptSize, int64(len(line)), idx.AuthoredTurns)
				var encoded []byte
				encoded, err = json.Marshal(entry)
				if err != nil {
					break
				}
				_, err = tx.ExecContext(ctx, `INSERT INTO entries VALUES(?,?,?,?,?,?,?)`, entry.Index, entry.Offset, entry.Length, entry.AuthoredTurn, entry.Role, users, encoded)
				if err != nil {
					break
				}
				encoded, err = json.Marshal(messageForSessionIdentity(message))
				if err != nil {
					break
				}
				_, _ = hash.Write(encoded)
				_, _ = hash.Write([]byte{'\n'})
				if entry.Role == provider.RoleUser && !entry.PinnedContextRevision {
					users++
				}
				if entry.StartsTurn && idx.ListingPreview == "" {
					idx.ListingPreview = truncatePreview(previewProse(UserMessageText(message)))
				}
				idx.MessageCount++
				idx.TranscriptSize += int64(len(line))
			}
			if errors.Is(readErr, io.EOF) {
				done = true
				break
			}
		}
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("build checkpoint display index: %w", err)
		}
		state, marshalErr := hash.(encoding.BinaryMarshaler).MarshalBinary()
		if marshalErr == nil {
			marshalErr = saveCheckpointPagerProgress(ctx, tx, idx, users, state)
		}
		if marshalErr != nil {
			_ = tx.Rollback()
			return marshalErr
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		if committed != nil {
			committed(idx.MessageCount)
		}
	}
	return publishCheckpointDisplayPager(ctx, db, source, f, fingerprint, idx, hash)
}

// Fence both the handle that supplied the bytes and its current pathname
// before publishing. A replacement or a newly authoritative event log cannot
// turn a resumed checkpoint prefix into a complete generation.
func validateCheckpointPagerFile(source string, f *os.File, fingerprint string) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	target, version := fileops.DiskHandleSnapshot(source, f, info)
	if fmt.Sprintf("%s:%s:checkpoint", target.Key, version) != fingerprint {
		return ErrDisplaySourceChanged
	}
	current, err := os.Stat(source)
	if err != nil {
		return err
	}
	target, version = fileops.DiskSnapshot(source, current)
	if !os.SameFile(info, current) || fmt.Sprintf("%s:%s:checkpoint", target.Key, version) != fingerprint {
		return ErrDisplaySourceChanged
	}
	return validateCheckpointDisplaySource(source)
}

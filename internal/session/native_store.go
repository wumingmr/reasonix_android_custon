package session

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func loadStartupSessionStateForManifest(ctx context.Context, dir, eventsPath string, manifest Manifest, externalHistory bool, recovery *recoveryStore, identity storageIdentity) (*startupSessionState, int64, bool, RecoveryOpenStats, bool, error) {
	if manifest.Codec == Codec {
		return loadStartupSessionState(ctx, dir, eventsPath, externalHistory, recovery, identity)
	}
	file, err := os.Open(eventsPath)
	if os.IsNotExist(err) {
		projection, _ := Project(nil)
		return &startupSessionState{projection: projection, operations: map[string]operationRecord{}}, 0, false, RecoveryOpenStats{}, false, nil
	}
	if err != nil {
		return nil, 0, false, RecoveryOpenStats{}, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, 0, false, RecoveryOpenStats{}, false, err
	}
	projection, _ := Project(nil)
	state := &startupSessionState{projection: projection, operations: map[string]operationRecord{}}
	var durableEnd int64
	var projectionErr error
	err = scanCommitFileCodecBoundaries(ctx, file, 0, 1, manifest.Codec, nil, func(offset, end int64, commit Commit) bool {
		if applyErr := applyProjectionCommit(&state.projection, commit); applyErr != nil {
			projectionErr = applyErr
			return false
		}
		if externalHistory {
			for _, message := range state.projection.Messages {
				if state.catalogPreview == "" {
					state.catalogPreview = catalogMessagePreview(message)
				}
			}
			state.projection.Messages = nil
		}
		state.operations[commit.OperationID] = compactOperationRecord(commit)
		state.durable = commit.LastSequence()
		durableEnd = end
		state.tip = durableTip{LogOffset: end, AnchorOffset: offset, AnchorFirst: commit.FirstSequence, AnchorCommitID: commit.ID, AnchorHash: commit.OperationHash}
		if applyErr := applyRecentCommit(&state.recentMessages, commit); applyErr != nil {
			projectionErr = applyErr
			return false
		}
		state.messageIDs.admit(commit)
		return true
	})
	if err != nil || projectionErr != nil {
		return nil, 0, false, RecoveryOpenStats{LogBytesTotal: info.Size(), LogBytesRead: durableEnd}, false, errors.Join(err, projectionErr)
	}
	state.projection.CommittedSequence = state.durable
	return state, durableEnd, durableEnd < info.Size(), RecoveryOpenStats{LogBytesTotal: info.Size(), LogBytesRead: durableEnd}, false, nil
}

func scanCommitFileCodec(file *os.File, startOffset int64, nextSequence uint64, codec string, knownKinds map[string]bool, visit func(int64, Commit) bool) error {
	return scanCommitFileCodecBoundaries(context.Background(), file, startOffset, nextSequence, codec, knownKinds, func(start, _ int64, commit Commit) bool {
		return visit == nil || visit(start, commit)
	})
}

// Record boundaries come from the original bytes, never re-encoded JSON: old
// writers may use different whitespace or field ordering.
func scanCommitFileCodecBoundaries(ctx context.Context, file *os.File, startOffset int64, nextSequence uint64, codec string, knownKinds map[string]bool, visit func(int64, int64, Commit) bool) error {
	if knownKinds == nil {
		knownKinds = ProjectionKinds
		if codec == PrototypeCodec {
			knownKinds = PrototypeProjectionKinds
		}
	}
	if _, err := file.Seek(startOffset, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(file, 64<<10)
	next := nextSequence
	offset := startOffset
	operations := map[string]string{}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		recordOffset := offset
		line, readErr := reader.ReadBytes('\n')
		if errors.Is(readErr, io.EOF) {
			// Cold readers expose only the complete durable prefix. The exclusive
			// writer path preserves and repairs this tail before accepting work.
			break
		}
		if readErr != nil {
			return readErr
		}
		offset += int64(len(line))
		var commit Commit
		if err := json.Unmarshal(bytes.TrimSuffix(line, []byte{'\n'}), &commit); err != nil {
			return fmt.Errorf("%w: decode complete commit: %w", ErrDamagedStore, err)
		}
		if commit.SchemaVersion != 3 || commit.Codec != codec {
			return fmt.Errorf("%w: event codec", ErrUnsupportedVersion)
		}
		if commit.RecordType != "commit" || commit.ID == "" || commit.OperationID == "" ||
			commit.OperationHash == "" || commit.WriterGeneration == 0 || commit.FirstSequence != next ||
			commit.EventCount != len(commit.Events) || commit.EventCount == 0 {
			return fmt.Errorf("%w: invalid commit boundary at sequence %d", ErrDamagedStore, next)
		}
		if prior, ok := operations[commit.OperationID]; ok && prior != commit.OperationHash {
			return fmt.Errorf("%w: conflicting operation %q", ErrDamagedStore, commit.OperationID)
		}
		operations[commit.OperationID] = commit.OperationHash
		for i, event := range commit.Events {
			if event.Sequence != next+uint64(i) || event.ID == "" || strings.TrimSpace(event.Kind) == "" {
				return fmt.Errorf("%w: invalid event at sequence %d", ErrDamagedStore, next+uint64(i))
			}
			if !event.Optional && !knownKinds[event.Kind] {
				return fmt.Errorf("%w: unknown required event %q", ErrUnsupportedVersion, event.Kind)
			}
		}
		next = commit.LastSequence() + 1
		normalizePrototypeHistory(&commit, codec)
		if visit != nil && !visit(recordOffset, offset, commit) {
			return nil
		}
	}
	return nil
}

func (opts OpenOptions) openContext() context.Context {
	if opts.Context != nil {
		return opts.Context
	}
	return context.Background()
}
func normalizePrototypeHistory(commit *Commit, codec string) {
	if codec != PrototypeCodec {
		return
	}
	for i := range commit.Events {
		if commit.Events[i].Kind == "context/replace" {
			commit.Events[i].Kind = "history/replace"
		}
	}
}
func loadStartupCatalogPreview(dir string, manifest Manifest, externalHistory bool, startup *startupSessionState) {
	if externalHistory && startup.catalogPreview == "" {
		revision, revisionErr := revisionOfLog(dir)
		cacheDir := filepath.Join(filepath.Dir(dir), ".query-cache", filepath.Base(dir))
		if revisionErr == nil {
			if metadata, metadataErr := readCatalogMetadata(cacheDir, manifest, revision); metadataErr == nil {
				startup.catalogPreview = metadata.Preview
			}
		}
	}
}

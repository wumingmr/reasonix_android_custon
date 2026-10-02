package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"

	"reasonix/internal/agent"
	"reasonix/internal/provider"
)

var ErrImportConflict = errors.New("legacy transcript and session event source conflict")

type ImportResult struct {
	TargetID string
	Source   Source
	Reused   bool
	Kind     string
}

// MigrationHistoryContains compares durable transcript meaning using the same
// normalization as paired imports. Callers must establish provenance first;
// matching text alone is not proof that two sessions share an identity.
func MigrationHistoryContains(history, prefix []provider.Message) bool {
	history, prefix = comparableImportMessages(history), comparableImportMessages(prefix)
	if len(prefix) > len(history) {
		return false
	}
	for index := range prefix {
		if !reflect.DeepEqual(history[index], prefix[index]) {
			return false
		}
	}
	return true
}

func importSourceForLegacy(ctx context.Context, sourcePath, targetRoot, headID string) (ImportResult, error) {
	return importSourceForLegacyWithHeader(ctx, sourcePath, targetRoot, headID, CreateOptions{})
}

func importSourceForLegacyWithHeader(ctx context.Context, sourcePath, targetRoot, headID string, options CreateOptions) (ImportResult, error) {
	return importSourceForLegacyFrom(ctx, sourcePath, targetRoot, targetRoot, headID, options)
}

func importSourceForLegacyFrom(ctx context.Context, sourcePath, sourceRoot, targetRoot, headID string, options CreateOptions) (ImportResult, error) {
	return importSourceForLegacyAt(ctx, sourcePath, filepath.Join(sourceRoot, agent.BranchID(sourcePath)), targetRoot, headID, options)
}

func importSourceForLegacyAt(ctx context.Context, sourcePath, previewDir, targetRoot, headID string, options CreateOptions) (ImportResult, error) {
	// The identity cutover deliberately reuses BranchID(sourcePath) for the
	// canonical runtime. Once a final v4 store exists at that identity it is
	// authoritative: treating it as a retired "paired preview" both rejects a
	// valid codec and can remigrate an older checkpoint over newer v4 work.
	if final, finalErr := readManifest(filepath.Join(previewDir, "manifest.json")); finalErr == nil {
		if final.SessionID != filepath.Base(previewDir) {
			return ImportResult{}, fmt.Errorf("session: canonical store identity %q does not match directory identity %q", final.SessionID, filepath.Base(previewDir))
		}
		source := Source{Path: sourcePath, Version: Codec}
		if final.Source != nil {
			source = *final.Source
		}
		if err := validateSessionHeaderForCreate(previewDir, final.SessionID, options); err != nil {
			return ImportResult{}, err
		}
		return ImportResult{TargetID: final.SessionID, Source: source, Reused: true, Kind: "final"}, nil
	}
	if _, statErr := os.Stat(previewDir); errors.Is(statErr, fs.ErrNotExist) {
		frozenLegacy, err := freezeLegacyHead(ctx, sourcePath, headID, true)
		if err != nil {
			return ImportResult{}, err
		}
		defer os.RemoveAll(frozenLegacy.freezeDir)
		return publishLegacyImportWithHeader(ctx, frozenLegacy, targetRoot, options)
	}
	// Freeze and parse every candidate before publication. Inspecting a paired
	// sidecar after publishing legacy history can omit newer work and leave an
	// adopted target behind after a refused import.
	frozenLegacy, err := freezeLegacyHead(ctx, sourcePath, headID, true)
	if err != nil {
		return ImportResult{}, err
	}
	defer os.RemoveAll(frozenLegacy.freezeDir)
	frozenPreview, err := freezePairedPreview(ctx, previewDir)
	if errors.Is(err, fs.ErrNotExist) {
		// No paired sidecar (or no target root yet) means the transcript is the
		// only candidate. Nothing has been published at this point.
		return publishLegacyImportWithHeader(ctx, frozenLegacy, targetRoot, options)
	}
	if err != nil {
		return ImportResult{}, fmt.Errorf("inspect paired session events: %w", err)
	}
	defer os.RemoveAll(frozenPreview.freezeDir)
	preview, meaningful, err := inspectFrozenPreview(ctx, frozenPreview)
	if err != nil {
		return ImportResult{}, fmt.Errorf("inspect paired session events: %w", err)
	}
	if !meaningful {
		return publishLegacyImportWithHeader(ctx, frozenLegacy, targetRoot, options)
	}

	relation, legacyMessages, firstDifference, err := compareLegacySpool(frozenLegacy.messageSpool, preview)
	if err != nil {
		return ImportResult{}, err
	}
	switch relation {
	case importMessagesEqual, importLegacyPrefix:
		// The event sidecar carries the same history or a strictly longer one,
		// so it is the only source that can be resumed without losing work.
		imported, importErr := importFrozenPreview(ctx, frozenPreview, targetRoot, options)
		return ImportResult{TargetID: imported.TargetID, Source: imported.Source, Reused: imported.Reused, Kind: "events"}, importErr
	case importPreviewPrefix:
		// The transcript is strictly newer; the sidecar is an earlier prefix.
		return publishLegacyImportWithHeader(ctx, frozenLegacy, targetRoot, options)
	default:
		// Neither source is a provable prefix of the other. Both originals stay
		// read-only and no executable target is created.
		return ImportResult{}, fmt.Errorf("%w: legacy=%s events=%s legacy_messages=%d event_messages=%d first_difference=%d", ErrImportConflict, sourcePath, frozenPreview.source.Version, legacyMessages, len(comparableImportMessages(preview)), firstDifference)
	}
}

type importMessageRelation uint8

const (
	importMessagesEqual importMessageRelation = iota
	importLegacyPrefix
	importPreviewPrefix
	importMessagesConflict
)

// compareLegacySpool proves the same prefix relation as the old in-memory
// comparison while retaining only one legacy message at a time.
func compareLegacySpool(path string, preview []provider.Message) (importMessageRelation, int, int, error) {
	preview = comparableImportMessages(preview)
	file, err := os.Open(path)
	if err != nil {
		return importMessagesConflict, 0, 0, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	legacyCount := 0
	firstDifference := -1
	for {
		var message provider.Message
		err := decoder.Decode(&message)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return importMessagesConflict, legacyCount, max(firstDifference, 0), err
		}
		comparable := comparableImportMessages([]provider.Message{message})
		if len(comparable) == 0 {
			continue
		}
		if firstDifference < 0 && legacyCount < len(preview) && !reflect.DeepEqual(comparable[0], preview[legacyCount]) {
			firstDifference = legacyCount
		}
		legacyCount++
	}
	if firstDifference >= 0 {
		return importMessagesConflict, legacyCount, firstDifference, nil
	}
	switch {
	case legacyCount == len(preview):
		return importMessagesEqual, legacyCount, legacyCount, nil
	case legacyCount < len(preview):
		return importLegacyPrefix, legacyCount, legacyCount, nil
	default:
		return importPreviewPrefix, legacyCount, len(preview), nil
	}
}

// publishLegacyImportWithHeader materializes the transcript target. It runs
// only after the source decision is final, so a refused or sidecar-winning
// import never creates a target or immutable Header as a side effect.
func publishLegacyImportWithHeader(ctx context.Context, frozen *frozenLegacyHead, targetRoot string, options CreateOptions) (ImportResult, error) {
	migration, err := frozen.publish(ctx, targetRoot, options)
	if err != nil {
		return ImportResult{}, err
	}
	return importResultFromLegacy(migration), nil
}

func importResultFromLegacy(result MigrationResult) ImportResult {
	return ImportResult{TargetID: result.TargetID, Source: result.Source, Reused: result.Reused, Kind: "legacy"}
}

func inspectFrozenPreview(ctx context.Context, frozen frozenPreview) ([]provider.Message, bool, error) {
	projection := Projection{}
	meaningful := false
	var projectionErr error
	knownKinds := ProjectionKinds
	if frozen.manifest.Codec == PrototypeCodec {
		knownKinds = PrototypeProjectionKinds
	}
	file, err := os.Open(frozen.eventPath)
	if err != nil {
		return nil, false, err
	}
	visit := func(_ int64, commit Commit) bool {
		if ctx.Err() != nil {
			return false
		}
		converted := cloneCommit(commit)
		for i := range converted.Events {
			kind := converted.Events[i].Kind
			if frozen.manifest.Codec == PrototypeCodec && kind == "context/replace" {
				converted.Events[i].Kind = "history/replace"
				kind = "history/replace"
			}
			switch kind {
			case "session/config", "session/title", "diagnostic":
			default:
				meaningful = true
			}
		}
		if applyErr := applyProjectionCommit(&projection, converted); applyErr != nil {
			projectionErr = applyErr
			return false
		}
		return true
	}
	if frozen.manifest.Codec == Codec && frozen.manifest.StorageRevision == 0 {
		err = scanV4CommitFile(ctx, file, 0, 1, contentStoreForSessionDir(frozen.dir), knownKinds, visit)
	} else {
		err = scanCommitFileCodec(file, 0, 1, frozen.manifest.Codec, knownKinds, visit)
	}
	_ = file.Close()
	if err != nil {
		return nil, false, err
	}
	if projectionErr != nil {
		return nil, false, projectionErr
	}
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	return projection.Messages, meaningful, nil
}

func messagesEqual(left, right []provider.Message) bool {
	return messageSequenceEqual(comparableImportMessages(left), comparableImportMessages(right))
}

func messagesPrefix(prefix, whole []provider.Message) bool {
	prefix = comparableImportMessages(prefix)
	whole = comparableImportMessages(whole)
	return len(prefix) <= len(whole) && messageSequenceEqual(prefix, whole[:len(prefix)])
}

func messageSequenceEqual(left, right []provider.Message) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if !reflect.DeepEqual(left[i], right[i]) {
			return false
		}
	}
	return true
}

// comparableImportMessages keeps stable identity and provider-visible work but
// removes process-local diagnostics. Legacy snapshots may record a completed
// tool recovery receipt after the typed event mirror has already committed the
// same call; that metadata cannot authorize resumed execution and must not turn
// identical work into a migration conflict.
func comparableImportMessages(messages []provider.Message) []provider.Message {
	messages = provider.ModelMessages(messages)
	out := append([]provider.Message(nil), messages...)
	for i := range out {
		out[i].MemoryCitations = nil
		out[i].WorkDurationMs = 0
		out[i].CreatedAt = 0
		out[i].Edited = false
		out[i].Original = ""
	}
	return out
}

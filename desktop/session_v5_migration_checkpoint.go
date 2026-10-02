package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"reasonix/internal/store"
)

// Migration may add members to an existing workspace, but it does not own the
// user's title or visibility. Avoid rewriting presentation for each new source.
func (a *App) ensureDesktopMigrationWorkspace(ctx context.Context, source desktopMigrationSource) (string, error) {
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return "", err
	}
	id := desktopWorkspaceOwnerID(state, source.scope, source.workspaceRoot)
	if _, exists := state.Workspaces[id]; exists {
		return id, nil
	}
	return a.ensureDesktopWorkspace(ctx, source.scope, source.workspaceRoot)
}

// A completed ledger entry records adoption of a source, not equality with
// today's target. The target can be continued, archived or deliberately deleted.
// None of those actions authorize importing the old conversation again.
type desktopMigrationCheckpoint struct {
	key         string
	files       []string
	revision    string
	record      desktopMigrationRecord
	refresh     bool
	inputDigest string
}

type desktopMigrationReceipt struct {
	extra           map[string]json.RawMessage
	TargetSessionID string `json:"targetSessionId"`
	ContentDigest   string `json:"contentDigest,omitempty"`
	SourceRevision  string `json:"sourceRevision,omitempty"`
}

func newDesktopMigrationCheckpoint(source desktopMigrationSource, key string, files []string) (desktopMigrationCheckpoint, error) {
	records := source.records
	if records == nil {
		ledger, err := readDesktopMigrationLedger()
		if err != nil {
			return desktopMigrationCheckpoint{}, err
		}
		records = ledger.Records
	}
	revision, err := desktopMigrationSourceRevision(files)
	if err != nil {
		return desktopMigrationCheckpoint{}, errors.Join(err, updateDesktopMigrationLedger(key, records[key].TargetSessionID, "failed", "source_stat"))
	}
	record := records[key]
	refresh := record.Status != "completed" && record.PreviousCompletion != nil
	if refresh {
		previous := record.PreviousCompletion
		record.Status, record.TargetSessionID = "completed", previous.TargetSessionID
		record.ContentDigest, record.SourceRevision = previous.ContentDigest, previous.SourceRevision
	}
	checkpoint := desktopMigrationCheckpoint{key: key, files: files, revision: revision, record: record, refresh: refresh}
	if !checkpoint.unchanged() {
		checkpoint.inputDigest, err = desktopMigrationInputDigest(files)
		if err != nil {
			return desktopMigrationCheckpoint{}, err
		}
	}
	return checkpoint, nil
}

func (c desktopMigrationCheckpoint) completed() bool {
	return c.record.Status == "completed" && c.record.TargetSessionID != ""
}

func (c desktopMigrationCheckpoint) unchanged() bool {
	return c.completed() && c.record.SourceRevision == c.revision
}

func (c desktopMigrationCheckpoint) skip() error {
	if c.refresh {
		return c.complete(c.record.TargetSessionID, c.record.ContentDigest)
	}
	return nil
}

// Old ledgers have no source revision. Compare their recorded source digest
// once, without comparing to a target that may already contain newer work.
func (c desktopMigrationCheckpoint) matchesCompletedContent(digest string) bool {
	return c.completed() && digest != "" && c.record.ContentDigest == digest
}

func (c desktopMigrationCheckpoint) complete(targetID, digest string) error {
	revision, err := c.verifiedRevision()
	if err != nil {
		return errors.Join(err, updateDesktopMigrationLedger(c.key, targetID, "failed", "source_changed", digest))
	}
	return updateDesktopMigrationLedger(c.key, targetID, "completed", "", digest, revision)
}

func (c desktopMigrationCheckpoint) verify() error {
	_, err := c.verifiedRevision()
	return err
}

func (c desktopMigrationCheckpoint) verifiedRevision() (string, error) {
	revision, err := desktopMigrationSourceRevision(c.files)
	if err == nil && revision != c.revision && c.inputDigest != "" {
		// A catalog repair can rewrite identical JSONL bytes and refresh only
		// derived listing fields. Verify the frozen input, not inode/mtime alone.
		current, digestErr := desktopMigrationInputDigest(c.files)
		if digestErr == nil && current == c.inputDigest {
			c.revision = revision
		}
		err = digestErr
	}
	if err != nil || revision != c.revision {
		return "", errors.Join(errors.New("desktop migration source changed during import"), err)
	}
	return revision, nil
}

func canonicalMigrationSourceFiles(root, sessionID string) []string {
	dir := filepath.Join(root, sessionID)
	// Content blobs are immutable and referenced by the event log. Disposable
	// indexes, checkpoints and lock files do not change the migration input.
	return []string{filepath.Join(dir, "manifest.json"), filepath.Join(dir, "events.frames"),
		filepath.Join(dir, "events.jsonl"), filepath.Join(dir, "header.json")}
}

func desktopCanonicalMigrationKey(root, sessionID string) string {
	digest := sha256.Sum256([]byte(canonicalRuntimeRoot(root) + "\x00" + sessionID))
	return hex.EncodeToString(digest[:])
}

func legacyMigrationSourceFiles(path string) []string {
	// Catalog reads may rebuild disposable indexes while an import is frozen.
	// Their publication is not a change to the legacy conversation. Keep all
	// durable sidecars (including ancestry/ownership metadata) in the stamp.
	files := []string{path}
	for _, sidecar := range store.SessionSidecarFiles(path) {
		if sidecar == store.SessionEventIndex(path) || sidecar == store.SessionDisplayIndex(path) || sidecar == store.SessionTranscriptProjection(path) {
			continue
		}
		files = append(files, sidecar)
	}
	return files
}

func desktopLegacyMigrationKey(path string) string {
	digest := sha256.Sum256([]byte(canonicalRuntimeRoot(path)))
	return hex.EncodeToString(digest[:])
}

// This is a cheap filesystem revision, not a content integrity checksum.
// Normal source writes change size or mtime; no history bodies are read here.
// Include absent files so creation/removal of a sidecar invalidates the stamp.
func desktopMigrationSourceRevision(files []string) (string, error) {
	paths := append([]string(nil), files...)
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			fmt.Fprintf(hash, "%q:missing\n", path)
			continue
		}
		if err != nil {
			return "", err
		}
		fmt.Fprintf(hash, "%q:%d:%d:%d\n", path, info.Mode(), info.Size(), info.ModTime().UnixNano())
	}
	return "stat-v1-" + hex.EncodeToString(hash.Sum(nil)), nil
}

func readDesktopMigrationLedger() (desktopMigrationLedger, error) {
	desktopMigrationMu.Lock()
	defer desktopMigrationMu.Unlock()
	ledger, _, err := readDesktopMigrationLedgerFile()
	return ledger, err
}

// Callers serialize reads and atomic replacement with desktopMigrationMu.
func readDesktopMigrationLedgerFile() (desktopMigrationLedger, []byte, error) {
	ledger := desktopMigrationLedger{Version: 1, Records: map[string]desktopMigrationRecord{}}
	body, err := os.ReadFile(desktopMigrationLedgerPath())
	if os.IsNotExist(err) {
		return ledger, nil, nil
	}
	if err != nil {
		return ledger, nil, err
	}
	if err := json.Unmarshal(body, &ledger); err != nil {
		return ledger, nil, err
	}
	if ledger.Version > 1 {
		return ledger, nil, fmt.Errorf("desktop migration ledger version %d is unsupported", ledger.Version)
	}
	if ledger.Records == nil {
		ledger.Records = map[string]desktopMigrationRecord{}
	}
	return ledger, body, nil
}

// Preserve unknown root/record fields when adding an optional revision to a v1
// ledger. Older writers can drop the revision; the digest fallback remains safe.
func marshalDesktopMigrationRecord(original []byte, ledger desktopMigrationLedger, key string) ([]byte, error) {
	root := map[string]json.RawMessage{}
	if len(original) > 0 {
		if err := json.Unmarshal(original, &root); err != nil {
			return nil, err
		}
	}
	if root == nil {
		root = map[string]json.RawMessage{}
	}
	records := map[string]map[string]json.RawMessage{}
	if body := root["records"]; len(body) > 0 {
		if err := json.Unmarshal(body, &records); err != nil {
			return nil, err
		}
	}
	if records == nil {
		records = map[string]map[string]json.RawMessage{}
	}
	fields := records[key]
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	for _, name := range []string{"sourceKey", "targetSessionId", "contentDigest", "sourceRevision", "previousCompletion", "status", "errorCode", "attempts", "legacyHeads", "legacySelectedHead", "legacyPrimaryHead", "legacyHeadsRevision", "legacyAdoption", "legacyConversions"} {
		delete(fields, name)
	}
	body, err := json.Marshal(ledger.Records[key])
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	records[key] = fields
	root["records"], err = json.Marshal(records)
	if err != nil {
		return nil, err
	}
	root["version"], err = json.Marshal(ledger.Version)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(root, "", "  ")
}

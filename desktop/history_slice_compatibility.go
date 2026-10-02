package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reasonix/internal/agent"
	"reasonix/internal/store"
	"strings"
)

// coldHistorySlice pages a session file with no running controller. Supported
// formats use the bounded native pager. Explicitly unsupported formats retain
// the compatibility adapter below, which may require a full display replay.
func (a *App) coldHistorySlice(sessionDir, path string, req HistorySliceRequest) (HistorySlice, error) {
	sessionDir, sessionPath, err := a.historyReadSource(sessionDir, path)
	if err != nil {
		return emptyHistorySlice(), err
	}
	info, err := os.Stat(sessionPath)
	if err != nil {
		return emptyHistorySlice(), err
	}
	if info.IsDir() {
		return emptyHistorySlice(), fmt.Errorf("not a session file: %s", sessionPath)
	}
	if page, ok, err := a.pagedColdHistorySlice(a.bootContext(), sessionDir, sessionPath, req); ok {
		return page, err
	}
	return a.compatibilityColdHistorySlice(sessionDir, sessionPath, info, req)
}

func (a *App) compatibilityColdHistorySlice(sessionDir, sessionPath string, info os.FileInfo, req HistorySliceRequest) (HistorySlice, error) {
	if historySessionLooksEventFormat(sessionPath) {
		// Legacy event-record format: stream-decode (constant memory) and page
		// the decoded rows. Only ancient sessions take this path.
		slice, err := coldEventHistorySlice(sessionPath, info, req)
		slice.Source = "scan"
		return slice, err
	}
	resolver := sessionDisplayResolver(sessionDir, sessionPath)
	indexPath := store.SessionDisplayIndex(sessionPath)
	idx, err := agent.LoadSessionDisplayIndex(indexPath)
	identity, identityKnown, identityErr := agent.SessionContentIdentity(sessionPath)
	if identityErr != nil {
		return emptyHistorySlice(), identityErr
	}
	if err == nil && compatibilityDisplayIndexValid(idx, identity, identityKnown, info, indexPath, sessionPath) {
		slice, pageErr := a.pageHistorySliceSource(coldHistorySliceSource(sessionPath, idx), req, resolver, sessionPlannerDisplayTurns(sessionDir, sessionPath), nil, sessionPath)
		if pageErr != nil {
			return emptyHistorySlice(), pageErr
		}
		slice.Source = "index"
		return slice, nil
	}

	// Scan the checkpoint and compare its digest with the ledger before event
	// replay. Missing sidecars remain readable without trusting same-size
	// anchor rewrites.
	scanned, scanErr := agent.ScanSessionDisplayIndex(sessionPath)
	if scanErr == nil {
		if !identityKnown || scanned.ContentDigest == identity.DigestHex {
			if identityKnown {
				scanned.Revision = identity.Revision
				scanned.RevisionKnown = identity.RevisionKnown
			}
			if writeErr := agent.WriteSessionDisplayIndex(store.SessionDisplayIndex(sessionPath), scanned); writeErr != nil {
				slog.Debug("desktop: history display index republish failed", "path", sessionPath, "err", writeErr)
			}
			slice, pageErr := a.pageHistorySliceSource(coldHistorySliceSource(sessionPath, scanned), req, resolver, sessionPlannerDisplayTurns(sessionDir, sessionPath), nil, sessionPath)
			if pageErr != nil {
				return emptyHistorySlice(), pageErr
			}
			slice.Source = "scan"
			return slice, nil
		}
	}

	// The event log is authoritative. During append-only saves its transcript
	// is newer than the compatibility .jsonl anchor, so scanning the anchor
	// would silently omit the tail even when a display index covers it.
	if eventInfo, statErr := os.Stat(store.SessionEventLog(sessionPath)); statErr == nil && !eventInfo.IsDir() && eventInfo.Size() > 0 {
		messages, state, repairable, loadErr := agent.LoadSessionDisplayMessages(sessionPath)
		if loadErr != nil {
			return emptyHistorySlice(), loadErr
		}
		if !repairable {
			return emptyHistorySlice(), agent.ErrSessionDisplayReadModelDamaged
		}
		src := newInMemoryHistorySliceSource(strings.TrimSuffix(filepath.Base(sessionPath), ".jsonl"), messages, resolver, state, true)
		slice, pageErr := a.pageHistorySliceSource(src, req, resolver, sessionPlannerDisplayTurns(sessionDir, sessionPath), nil, sessionPath)
		if pageErr != nil {
			return emptyHistorySlice(), pageErr
		}
		slice.Source = "event-log"
		return slice, nil
	}

	// Legacy checkpoints have no authoritative ledger identity. Scan their
	// bytes to obtain the digest before trusting (or republishing) offsets; this
	// detects same-size external rewrites that a size-only comparison misses.
	if scanErr != nil {
		// The bounded scanner rejects malformed or oversized records. Preserve
		// compatibility through the authoritative loader; later reads can use
		// the file-exact index.
		messages, state, repairable, loadErr := agent.LoadSessionDisplayMessages(sessionPath)
		if loadErr != nil {
			return emptyHistorySlice(), errors.Join(scanErr, loadErr)
		}
		if !repairable {
			return emptyHistorySlice(), agent.ErrSessionDisplayReadModelDamaged
		}
		src := newInMemoryHistorySliceSource(strings.TrimSuffix(filepath.Base(sessionPath), ".jsonl"), messages, resolver, state, true)
		slice, pageErr := a.pageHistorySliceSource(src, req, resolver, sessionPlannerDisplayTurns(sessionDir, sessionPath), nil, sessionPath)
		if pageErr != nil {
			return emptyHistorySlice(), pageErr
		}
		slice.Source = "scan"
		return slice, nil
	}
	if identityKnown {
		if scanned.ContentDigest == identity.DigestHex {
			scanned.Revision = identity.Revision
			scanned.RevisionKnown = identity.RevisionKnown
		}
	}
	if writeErr := agent.WriteSessionDisplayIndex(store.SessionDisplayIndex(sessionPath), scanned); writeErr != nil {
		slog.Debug("desktop: history display index republish failed", "path", sessionPath, "err", writeErr)
	}
	slice, pageErr := a.pageHistorySliceSource(coldHistorySliceSource(sessionPath, scanned), req, resolver, sessionPlannerDisplayTurns(sessionDir, sessionPath), nil, sessionPath)
	if pageErr != nil {
		return emptyHistorySlice(), pageErr
	}
	slice.Source = "scan"
	return slice, nil
}

func compatibilityDisplayIndexValid(idx *agent.SessionDisplayIndex, identity agent.PersistedState, identityKnown bool, info os.FileInfo, indexPath, sessionPath string) bool {
	if idx == nil || idx.TranscriptSize != info.Size() {
		return false
	}
	// Without a ledger, publication order and exact size bind the index to
	// its checkpoint. A rewrite must also pass the timestamp guard.
	valid := !idx.RevisionKnown
	if identityKnown {
		valid = agent.ValidateSessionDisplayIndex(idx, identity.Revision, identity.RevisionKnown, identity.Digest, info.Size())
	}
	return valid && historyIndexTimestampValid(indexPath, sessionPath, info, idx, true)
}

package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Content refs from a bound window never fall back to whichever source or
// controller now occupies the tab. Missing/released handles are stale reads.
func (a *App) boundNativeHistoryContent(tabID string, ref HistoryContentRef, chunkIndex int) HistoryContentChunk {
	out := HistoryContentChunk{EntryID: ref.EntryID, Field: ref.Field, Chunk: max(chunkIndex, 0), Stale: true}
	r, err := a.historyReader(ref.ReadHandleID)
	if err != nil || r.tabID != tabID || r.native == nil || entryIDSession(ref.EntryID) != strings.TrimSuffix(filepath.Base(r.path), ".jsonl") {
		return out
	}
	position, sub, legacyRow, ok := parseHistoryEntryID(ref.EntryID)
	if !ok || legacyRow != -1 {
		return out
	}
	pager, err := nativeNavigationPager(r)
	if err != nil {
		return out
	}
	src := historySourceFromPager(r.ctx, pager, r.path, r.native.key)
	if ref.EntryID != fmt.Sprintf("s%s:r%d:m%d:o%d", src.sessionID, src.epoch, position, sub) {
		return out
	}
	dir := filepath.Dir(r.path)
	value, found, stale := a.historyFieldValueForSource(src, position, sub, ref, sessionDisplayResolver(dir, r.path), sessionPlannerDisplayTurns(dir, r.path), nil)
	if stale || !found || src.readErr != nil || len(value) != ref.Size || pager.Validate() != nil || r.ctx.Err() != nil || !a.historyReaderCurrent(r) {
		return out
	}
	out.Data, out.Chunks = historyContentChunkAt(value, chunkIndex)
	out.Done, out.Stale = chunkIndex >= out.Chunks-1, false
	return out
}

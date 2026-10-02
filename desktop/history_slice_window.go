package main

import "sort"

func historySliceCandidateRange(src *historySliceSource, req HistorySliceRequest, cursor historySliceCursor, hi int, forward bool) (int, int, error) {
	// Turn budget: the oldest visible turn this page may reach.
	newestTurn := src.turnAt(hi - 1)
	oldestTurn := 0
	if newestTurn > 0 {
		oldestTurn = max(newestTurn-req.Turns+1, 1)
	}
	// turns is non-decreasing: binary-search the first message in the page.
	candidateLo := sort.Search(hi, func(i int) bool { return src.turnAt(i) >= oldestTurn })
	if oldestTurn <= 1 {
		// A page reaching the first turn also includes the pre-turn messages
		// (system prompt), mirroring providerMessagesForVisibleTurnRange.
		candidateLo = 0
	}
	if src.maxFetch > 0 {
		candidateLo = max(candidateLo, hi-min(req.Entries, src.maxFetch))
	}
	if forward {
		candidateLo = cursor.Before
	}
	if src.readErr != nil {
		return 0, 0, src.readErr
	}
	// Cold-path raw-span cap: shrink the window forward while the byte span
	// is excessive (image-dense windows).
	if src.windowBytes != nil {
		for candidateLo < hi-1 && src.windowBytes(candidateLo, hi) > historySliceColdWindowBytes {
			if forward {
				hi--
			} else {
				candidateLo++
			}
		}
	}

	return candidateLo, hi, nil
}

func completeHistorySlicePage(page HistorySlice, src *historySliceSource, pageStart, hi int, sourceID string) HistorySlice {
	for _, e := range page.Entries {
		if e.Turn <= 0 {
			continue
		}
		if page.StartTurn == 0 || e.Turn < page.StartTurn {
			page.StartTurn = e.Turn
		}
		if e.Turn > page.EndTurn {
			page.EndTurn = e.Turn
		}
	}
	page.HasOlder = pageStart > 0
	if page.HasOlder {
		page.NextCursor = encodeHistorySliceCursor(historySliceCursor{
			V:        1,
			Revision: src.revision,
			RevKnown: src.revKnown,
			Digest:   src.digest,
			Before:   pageStart,
			Source:   sourceID,
		})
	}
	page.HasNewer = hi < src.total
	if page.HasNewer {
		page.NewerCursor = encodeHistorySliceCursor(historySliceCursor{V: 1, Revision: src.revision, RevKnown: src.revKnown, Digest: src.digest, Before: hi, Source: sourceID})
	}
	return page
}

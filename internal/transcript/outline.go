package transcript

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"reasonix/internal/textutil"
)

const (
	// An outline page carries metadata only: entries are small and fixed-shape,
	// so the entry count is the primary bound and the byte budget is a safety
	// net shared with the record pages' response limit.
	defaultOutlineEntries = 1000
	maxOutlineEntries     = 1000
	defaultOutlineBytes   = 512 << 10

	// Previews are display-only. The prompt cap keeps a rail label readable; the
	// answer cap keeps a hover card readable. Both are grapheme-cluster counts.
	promptPreviewRunes = 50
	answerPreviewRunes = 120

	// previewScanBytesPerCluster bounds the text inspected before collapsing.
	// 32 bytes covers every realistic cluster (a ZWJ family is ~25), so the
	// clamp never shortens a preview that the grapheme budget would have kept.
	previewScanBytesPerCluster = 32
)

// OutlineRequest pages the turn index bound to one snapshot.
type OutlineRequest struct {
	SnapshotID string `json:"snapshotId"`
	Offset     int    `json:"offset"`
	Entries    int    `json:"entries"`
	Bytes      int    `json:"bytes"`
}

// OutlineEntry is one user turn of the complete conversation. ID is the stable
// record identity shared with the body records, so a turn keeps its identity
// across snapshots. Order is only this snapshot's pagination position: it is a
// locator hint, never cross-snapshot identity and never a React key.
type OutlineEntry struct {
	ID        string `json:"id"`
	MessageID string `json:"messageId,omitempty"`
	Turn      int    `json:"turn"`
	Order     int    `json:"order"`
	Prompt    string `json:"prompt"`
	Answer    string `json:"answer,omitempty"`
}

// OutlinePage carries the same boundary as the record pages it describes, so a
// client can prove both reads belong to one immutable cut. An evicted cut
// answers Stale instead of silently continuing against the newest revision.
type OutlinePage struct {
	Boundary
	Entries    []OutlineEntry `json:"entries"`
	NextOffset int            `json:"nextOffset"`
	Done       bool           `json:"done"`
	Total      int            `json:"total"`
	Stale      bool           `json:"stale"`
}

// Outline returns a bounded page of the turn index for one snapshot. The index
// is complete regardless of how much body a client has paged in, so navigation
// does not depend on the loaded prefix.
func (p *Projection) Outline(req OutlineRequest) (OutlinePage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	frozen, err := p.freezeLocked(req.SnapshotID)
	if err != nil {
		return OutlinePage{}, err
	}
	if frozen == nil {
		return OutlinePage{Boundary: p.boundaryLocked(), Entries: []OutlineEntry{}, Stale: true}, nil
	}
	return frozen.outlineCurrent(req)
}

func (p *Projection) outlineCurrent(req OutlineRequest) (OutlinePage, error) {
	out := OutlinePage{Boundary: p.boundaryLocked(), Entries: []OutlineEntry{}}
	if req.SnapshotID != "" && req.SnapshotID != out.SnapshotID {
		out.Stale = true
		return out, nil
	}
	out.Total = len(p.outline)
	offset := min(max(req.Offset, 0), out.Total)
	limit := req.Entries
	if limit <= 0 {
		limit = defaultOutlineEntries
	}
	limit = min(limit, maxOutlineEntries)
	budget := req.Bytes
	if budget <= 0 {
		budget = defaultOutlineBytes
	}
	budget = min(budget, MaxResponseBytes)
	used := 0
	for i := offset; i < out.Total && len(out.Entries) < limit; i++ {
		encoded, err := json.Marshal(p.outline[i])
		if err != nil {
			return OutlinePage{}, err
		}
		// Always advance: a client that stopped on a full page would otherwise
		// retry the same offset forever.
		if len(out.Entries) > 0 && used+len(encoded) > budget {
			break
		}
		out.Entries = append(out.Entries, p.outline[i])
		used += len(encoded)
	}
	out.NextOffset = offset + len(out.Entries)
	out.Done = out.NextOffset >= out.Total
	encoded, err := json.Marshal(out)
	if err != nil {
		return OutlinePage{}, err
	}
	if len(encoded)+1 > MaxResponseBytes {
		return OutlinePage{}, errors.New("transcript outline metadata exceeds the page limit")
	}
	return out, nil
}

// buildOutline indexes every user turn in a frozen cut. One backward pass
// assigns each turn the last non-empty assistant body of its own group, so the
// index shares the records' identity and order without a second grouping model.
func buildOutline(messages []*bufferedMessage) []OutlineEntry {
	out := make([]OutlineEntry, 0, 16)
	answer := ""
	for i, row := range slices.Backward(messages) {
		switch row.message.Role {
		case "assistant":
			if answer == "" {
				answer = previewText(row.body(), answerPreviewRunes)
			}
		case "user":
			out = append(out, OutlineEntry{
				ID: row.message.RecordID, MessageID: row.message.MessageID,
				Turn: row.message.HistoryTurn, Order: i,
				Prompt: previewText(row.body(), promptPreviewRunes), Answer: answer,
			})
			answer = ""
		}
	}
	slices.Reverse(out)
	// A legacy turn row can reach here without an assigned ordinal. Keep the
	// rail monotonic instead of rendering turn 0 or renumbering later turns.
	previous := 0
	for i := range out {
		if out[i].Turn <= previous {
			out[i].Turn = previous + 1
		}
		previous = out[i].Turn
	}
	return out
}

// body reads display text without copying. Streaming assistants keep their text
// in the accumulator; every other row carries it on the message.
func (m *bufferedMessage) body() string {
	if m.message.Role == "assistant" {
		return m.content.string()
	}
	return m.message.Content
}

// previewText collapses whitespace and clamps to limit grapheme clusters. The
// input is bounded before collapsing so a multi-megabyte answer costs one short
// pass rather than a full scan per freeze. Only display bodies reach here:
// reasoning, tool output, submitted text and injected context are never part of
// an outline entry.
func previewText(text string, limit int) string {
	if end := (limit + 1) * previewScanBytesPerCluster; len(text) > end {
		text = text[:runeBoundary(text, end)]
	}
	collapsed := strings.Join(strings.Fields(text), " ")
	if collapsed == "" {
		return ""
	}
	return textutil.TruncateGraphemes(collapsed, limit, "…")
}

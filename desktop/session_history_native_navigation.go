package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"reasonix/internal/agent"
	"reasonix/internal/session"
	"reasonix/internal/textutil"
)

func nativeHistorySequence(p *agent.DisplayPager) uint64 {
	if p.Header.RevisionKnown && p.Header.Revision > 0 {
		return uint64(p.Header.Revision)
	}
	return 0
}

func nativeHistoryEntryID(r *desktopHistoryReader, p *agent.DisplayPager, position int) string {
	return fmt.Sprintf("s%s:r%d:m%d:o0", strings.TrimSuffix(filepath.Base(r.path), ".jsonl"), nativeHistorySequence(p), position)
}

func nativeNavigationPager(r *desktopHistoryReader) (*agent.DisplayPager, error) {
	if r.native == nil {
		return nil, agent.ErrDisplayFormatUnsupported
	}
	p, err := r.native.wait(r.ctx)
	if err != nil {
		return nil, err
	}
	p = p.WithContext(r.ctx)
	return p, p.Validate()
}

func nativeNavigationStatus(r *desktopHistoryReader, err error) string {
	if r.ctx.Err() != nil || errors.Is(err, agent.ErrDisplaySourceChanged) {
		return "stale_cursor"
	}
	if errors.Is(err, agent.ErrDisplayFormatUnsupported) {
		return "unsupported"
	}
	return "failed"
}

func nativeHistoryCursor(r *desktopHistoryReader, p *agent.DisplayPager, before int) string {
	return encodeHistorySliceCursor(historySliceCursor{V: 1, Revision: p.Header.Revision, RevKnown: p.Header.RevisionKnown,
		Digest: p.Header.ContentDigest, Before: before, Source: r.native.key})
}

func nativeHistoryLocation(r *desktopHistoryReader, p *agent.DisplayPager, messageID string, snapshot uint64) (session.MessageLocation, error) {
	sequence := nativeHistorySequence(p)
	location := session.MessageLocation{Status: "not_found", MessageID: messageID, Generation: r.native.key, SnapshotSequence: sequence, CoverageSequence: sequence}
	if snapshot != 0 && snapshot != sequence {
		location.Status = "stale_cursor"
		return location, nil
	}
	position, sub, legacyRow, ok := parseHistoryEntryID(messageID)
	// Cold outline identities name the first display row of a provider message.
	// Require an exact round trip; permissive Sscanf parsing alone is not proof
	// of this source, rewrite epoch, or an existing display identity.
	if !ok || sub != 0 || legacyRow != -1 || position < 0 || messageID != nativeHistoryEntryID(r, p, position) {
		return location, nil
	}
	entry, err := p.Entry(position)
	if errors.Is(err, sql.ErrNoRows) {
		return location, nil
	}
	if err != nil {
		return location, err
	}
	location.Status, location.Position, location.VisibleTurn = "ready", int64(position), entry.AuthoredTurn
	location.Cursor = nativeHistoryCursor(r, p, position+1)
	return location, p.Validate()
}

func (a *App) readNativeHistoryOutline(r *desktopHistoryReader, req session.HistoryOutlineRequest) (session.HistoryOutlinePage, error) {
	page := session.HistoryOutlinePage{Entries: []session.HistoryOutlineEntry{}}
	p, err := nativeNavigationPager(r)
	if err != nil {
		page.Status = nativeNavigationStatus(r, err)
		if page.Status != "failed" {
			err = nil
		}
		return page, err
	}
	page.Generation, page.SnapshotSequence, page.CoverageSequence = r.native.key, nativeHistorySequence(p), nativeHistorySequence(p)
	page.TotalTurns, page.NextTurn = p.Header.AuthoredTurns, max(req.StartTurn, 1)
	if req.Generation != "" && req.Generation != page.Generation || req.SnapshotSequence != nil && *req.SnapshotSequence != page.SnapshotSequence {
		page.Status = "stale_cursor"
		return page, nil
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 128
	}
	entries, err := p.TurnEntries(page.NextTurn, min(limit, 1000))
	if err != nil {
		return page, err
	}
	for _, entry := range entries {
		// Only requested prompt positions are read, one at a time. Never retain
		// the entire transcript or decode tool/attachment records between turns.
		var prompt string
		if p.DAG || p.SchemaOne {
			messages, readErr := p.EventMessages(entry.Index, entry.Index+1)
			err = readErr
			if err == nil && len(messages) == 1 {
				prompt = agent.UserMessageText(messages[0])
			}
		} else {
			messages, readErr := readSessionMessagesAtOffsetsContext(r.ctx, r.path, []agent.DisplayIndexEntry{entry})
			err = readErr
			if err == nil && len(messages) == 1 {
				prompt = agent.UserMessageText(messages[0])
			}
		}
		if err != nil {
			return page, err
		}
		prompt, err = nativeHistoryPrompt(r.ctx, prompt)
		if err != nil {
			return page, err
		}
		page.Entries = append(page.Entries, session.HistoryOutlineEntry{MessageID: nativeHistoryEntryID(r, p, entry.Index), Turn: entry.AuthoredTurn, Position: int64(entry.Index), Prompt: prompt})
		page.NextTurn = entry.AuthoredTurn + 1
	}
	if err := p.Validate(); err != nil {
		return session.HistoryOutlinePage{Entries: []session.HistoryOutlineEntry{}, Status: nativeNavigationStatus(r, err)}, err
	}
	page.Status, page.Done = "ready", page.NextTurn > page.TotalTurns
	return page, nil
}

// Normalize only a bounded prefix. strings.Fields over a multi-megabyte
// prompt would allocate an array for every word before clipping the preview.
func nativeHistoryPrompt(ctx context.Context, text string) (string, error) {
	var out strings.Builder
	space, count, nextCheck := false, 0, 0
	for offset, ch := range text {
		if offset >= nextCheck {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			nextCheck = offset + (64 << 10)
		}
		if unicode.IsSpace(ch) {
			space = out.Len() > 0
			continue
		}
		if space {
			out.WriteByte(' ')
			space = false
		}
		out.WriteRune(ch)
		count++
		if count >= 512 {
			out.WriteString("…")
			break
		}
	}
	return textutil.ClipGraphemes(out.String(), 50, "…"), ctx.Err()
}

// Resolve a direct jump using the same generation as paging and outline.
// Cursor navigation never walks pages from the newest end to find a target.
func nativeHistorySliceAnchor(r *desktopHistoryReader, p *agent.DisplayPager, req HistorySliceRequest) (HistorySliceRequest, string, error) {
	if err := p.WithContext(r.ctx).Validate(); err != nil {
		return req, nativeNavigationStatus(r, err), err
	}
	if req.Generation != "" && req.Generation != r.native.key || req.SnapshotSequence != nil && *req.SnapshotSequence != nativeHistorySequence(p) {
		return req, "stale_cursor", nil
	}
	switch req.Anchor {
	case "", "newest", "cursor":
		return req, "ready", nil
	case "turn":
		entries, err := p.WithContext(r.ctx).TurnEntries(max(req.Turn, 1), 1)
		if err != nil {
			return req, "failed", err
		}
		if len(entries) == 0 || entries[0].AuthoredTurn != max(req.Turn, 1) {
			return req, "not_found", nil
		}
		req.Cursor = nativeHistoryCursor(r, p, entries[0].Index+1)
	case "message":
		location, err := nativeHistoryLocation(r, p.WithContext(r.ctx), req.MessageID, 0)
		if err != nil || location.Status != "ready" {
			return req, location.Status, err
		}
		req.Cursor = location.Cursor
	default:
		return req, "unsupported", nil
	}
	req.Newer = false
	return req, "ready", nil
}

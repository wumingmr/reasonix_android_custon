package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"reasonix/internal/agent"
	"reasonix/internal/provider"
	"reasonix/internal/sessioncontent"
)

// TitleMessages reads the first authored turns from the durable UI history.
// Turn anchors avoid replaying large tool outputs or using compacted model
// context. Referenced bodies use the same bounded content reader as history.
func (q *Query) TitleMessages(ctx context.Context, ref SessionRef, limit int) ([]provider.Message, error) {
	if q == nil {
		return nil, errors.New("session: nil query")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ref.validate(q.hostID); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return []provider.Message{}, nil
	}
	filesystem, ok := q.persistence.(*FilesystemPersistence)
	if !ok {
		return nil, fmt.Errorf("session: title history requires filesystem persistence")
	}
	preparation := q.prepareHistoryLocator(filesystem, ref.SessionID, historyIndexPath(filesystem.Root, ref.SessionID), ctx)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-preparation.done:
		if preparation.err != nil {
			return nil, preparation.err
		}
	}
	baseline, err := q.ReadHistoryWindow(ctx, ref, HistoryWindowRequest{Anchor: "newest", Limit: 1})
	if err != nil {
		return nil, err
	}
	if baseline.Status != "ready" {
		return nil, fmt.Errorf("session: title history is %s", baseline.Status)
	}
	messages := make([]provider.Message, 0, limit)
	for turn := 1; turn <= baseline.TotalTurns && len(messages) < limit; turn++ {
		page, err := q.ReadHistoryWindow(ctx, ref, HistoryWindowRequest{
			Anchor: "turn", Turn: turn, Limit: 1, SnapshotSequence: &baseline.SnapshotSequence, Generation: baseline.Generation,
		})
		if err != nil {
			return nil, err
		}
		if page.Status != "ready" {
			return nil, fmt.Errorf("session: title history is %s", page.Status)
		}
		for _, entry := range page.Messages {
			body := entry.Inline
			if entry.ContentRef != nil {
				if entry.ContentRef.Bytes > sessioncontent.MaxReadRange {
					return nil, fmt.Errorf("session: title message exceeds the history body read budget")
				}
				body = nil
				for offset := int64(0); offset < entry.ContentRef.Bytes; {
					chunk, err := q.ReadContent(ctx, ref, *entry.ContentRef, offset, min(1<<20, entry.ContentRef.Bytes-offset))
					if err != nil {
						return nil, err
					}
					if len(chunk) == 0 {
						return nil, fmt.Errorf("session: incomplete title message body")
					}
					body = append(body, chunk...)
					offset += int64(len(chunk))
				}
			}
			var message provider.Message
			if err := json.Unmarshal(body, &message); err != nil {
				return nil, fmt.Errorf("session: decode title message: %w", err)
			}
			if agent.IsUserAuthoredTurnMessage(message) {
				messages = append(messages, message)
			}
		}
	}
	return messages, nil
}

package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"reasonix/internal/agent"
	"reasonix/internal/provider"
)

func (a *App) pagedColdHistorySlice(ctx context.Context, sessionDir, path string, req HistorySliceRequest, heads ...string) (HistorySlice, bool, error) {
	head := ""
	if len(heads) > 0 {
		head = heads[0]
	}
	page, handled := emptyHistorySlice(), false
	err := a.withNativeHistoryPager(ctx, path, head, func(ctx context.Context, pager *agent.DisplayPager, sourceID string) error {
		handled = true
		var err error
		page, _, err = a.historySliceFromPager(ctx, pager, sessionDir, path, req, sourceID)
		return err
	})
	if handled || nativeHistoryPreparationFailure(err) {
		if err != nil {
			return emptyHistorySlice(), true, err
		}
		return page, true, nil
	}
	return HistorySlice{}, false, nil
}

func nativeHistoryPreparationFailure(err error) bool {
	// Only a positively identified unsupported format may use the legacy
	// adapter. I/O, parse and cache failures must not silently trigger a full
	// replay after the bounded reader has failed.
	return err != nil && !errors.Is(err, agent.ErrDisplayFormatUnsupported)
}

// Compatibility paging and field reads borrow the same preparation and cache
// owner as bound readers. Retirement cancels both the read and its decoding;
// only the last borrower retires the projection, after the callback returns.
func (a *App) withNativeHistoryPager(ctx context.Context, path, head string, read func(context.Context, *agent.DisplayPager, string) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	generation, err := nativeHistorySourceGeneration(path)
	if err != nil {
		return errors.Join(agent.ErrDisplaySourceChanged, err)
	}
	sourceKey := sessionRuntimeKey(path)
	a.historyReaders.mu.Lock()
	if a.historyReaders.closed || a.shuttingDown.Load() {
		a.historyReaders.mu.Unlock()
		return context.Canceled
	}
	job, release := a.acquireNativeHistoryLocked(path, head, sourceKey, generation)
	a.historyReaders.mu.Unlock()
	defer release()
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(job.ctx, cancel)
	defer func() { stop(); cancel() }()
	pager, err := job.wait(ctx)
	if err != nil {
		return err
	}
	pager = pager.WithContext(ctx)
	if err := pager.Validate(); err != nil {
		return err
	}
	err = read(ctx, pager, job.key)
	if ctx.Err() != nil || job.ctx.Err() != nil {
		return context.Canceled
	}
	if err != nil {
		return err
	}
	return pager.Validate()
}

func (a *App) historySliceFromPager(ctx context.Context, pager *agent.DisplayPager, sessionDir, path string, req HistorySliceRequest, sourceID string) (HistorySlice, bool, error) {
	src := historySourceFromPager(ctx, pager, path, sourceID)
	page, err := a.pageHistorySliceSource(src, req, sessionDisplayResolver(sessionDir, path), sessionPlannerDisplayTurns(sessionDir, path), nil, path)
	if src.readErr != nil {
		return emptyHistorySlice(), true, src.readErr
	}
	page.Source = "index"
	if pager.Built {
		page.Source = "scan"
	}
	if pager.DAG || pager.SchemaOne {
		page.Source = "event-log"
	}
	return page, true, err
}

func historySourceFromPager(ctx context.Context, pager *agent.DisplayPager, path, sourceID string) *historySliceSource {
	pager = pager.WithContext(ctx)
	idx := &pager.Header
	src := &historySliceSource{sessionID: strings.TrimSuffix(filepath.Base(path), ".jsonl"), total: idx.MessageCount,
		totalTurns: idx.AuthoredTurns, revision: idx.Revision, revKnown: idx.RevisionKnown, digest: idx.ContentDigest,
		position: pager.Entry, usersBefore: pager.UsersBefore, maxFetch: 500, windowOnly: true}
	src.sourceID = sourceID
	if idx.RevisionKnown {
		src.epoch = int(idx.Revision)
	} else {
		src.revision = 0
	}
	src.fetch = func(lo, hi int) ([]provider.Message, error) {
		if err := pager.Validate(); err != nil {
			return nil, err
		}
		if pager.DAG || pager.SchemaOne {
			return pager.EventMessages(lo, hi)
		}
		entries, err := pager.Entries(lo, hi)
		if err != nil {
			return nil, err
		}
		messages, err := readSessionMessagesAtOffsetsContext(ctx, path, entries)
		if err != nil {
			return nil, err
		}
		return messages, pager.Validate()
	}
	src.windowBytes = func(lo, hi int) int64 {
		if hi <= lo {
			return 0
		}
		if pager.DAG || pager.SchemaOne {
			entries, err := pager.Entries(lo, hi)
			if err != nil {
				src.readErr = err
				return 0
			}
			var total int64
			for _, entry := range entries {
				total += entry.Length
			}
			return total
		}
		first, err := pager.Entry(lo)
		if err != nil {
			src.readErr = err
			return 0
		}
		last, err := pager.Entry(hi - 1)
		if err != nil {
			src.readErr = err
			return 0
		}
		return last.Offset + last.Length - first.Offset
	}
	return src
}

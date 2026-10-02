package cdp

import (
	"context"
	"sync"

	"reasonix/internal/browser"
)

// Lazy is the executor a session is configured with but has not paid for yet.
// Attaching means launching or connecting to a browser, which no session
// should do just because the tools are registered, so the browser appears on
// the first tool call and not before.
type Lazy struct {
	opts Options

	mu   sync.Mutex
	exec *Executor
	err  error
}

var _ browser.Executor = (*Lazy)(nil)

// NewLazy returns an executor that attaches on first use.
func NewLazy(opts Options) *Lazy { return &Lazy{opts: opts} }

// ensure attaches once. A start failure is remembered and returned to every
// later call, so a missing browser costs one launch timeout and not one per
// tool call; a caller who cancelled their own context leaves no such verdict.
func (l *Lazy) ensure(ctx context.Context) (*Executor, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.exec != nil {
		return l.exec, nil
	}
	if l.err != nil {
		return nil, l.err
	}
	exec, err := New(ctx, l.opts)
	if err != nil {
		if ctx.Err() == nil {
			l.err = err
		}
		return nil, err
	}
	l.exec = exec
	return exec, nil
}

// Available keeps an unattached executor visible: the capability exists, and
// the first call is what reports why a browser could not be reached.
func (l *Lazy) Available(ctx context.Context) bool {
	l.mu.Lock()
	exec, err := l.exec, l.err
	l.mu.Unlock()
	if exec != nil {
		return exec.Available(ctx)
	}
	return err == nil
}

// Shutdown releases the browser if one was ever attached.
func (l *Lazy) Shutdown() {
	l.mu.Lock()
	exec := l.exec
	l.exec = nil
	l.mu.Unlock()
	if exec != nil {
		exec.Shutdown()
	}
}

func (l *Lazy) Tabs(ctx context.Context) ([]browser.Tab, error) {
	exec, err := l.ensure(ctx)
	if err != nil {
		return nil, err
	}
	return exec.Tabs(ctx)
}

func (l *Lazy) Open(ctx context.Context, req browser.OpenRequest) (browser.Tab, error) {
	exec, err := l.ensure(ctx)
	if err != nil {
		return browser.Tab{}, err
	}
	return exec.Open(ctx, req)
}

func (l *Lazy) Navigate(ctx context.Context, req browser.NavigateRequest) (browser.Tab, error) {
	exec, err := l.ensure(ctx)
	if err != nil {
		return browser.Tab{}, err
	}
	return exec.Navigate(ctx, req)
}

func (l *Lazy) Snapshot(ctx context.Context, req browser.SnapshotRequest) (browser.Snapshot, error) {
	exec, err := l.ensure(ctx)
	if err != nil {
		return browser.Snapshot{}, err
	}
	return exec.Snapshot(ctx, req)
}

func (l *Lazy) Screenshot(ctx context.Context, req browser.ScreenshotRequest) (browser.Screenshot, error) {
	exec, err := l.ensure(ctx)
	if err != nil {
		return browser.Screenshot{}, err
	}
	return exec.Screenshot(ctx, req)
}

func (l *Lazy) Act(ctx context.Context, req browser.ActRequest) (browser.ActResult, error) {
	exec, err := l.ensure(ctx)
	if err != nil {
		return browser.ActResult{}, err
	}
	return exec.Act(ctx, req)
}

func (l *Lazy) Downloads(ctx context.Context, req browser.DownloadsRequest) ([]browser.Download, error) {
	exec, err := l.ensure(ctx)
	if err != nil {
		return nil, err
	}
	return exec.Downloads(ctx, req)
}

func (l *Lazy) Close(ctx context.Context, req browser.CloseRequest) error {
	exec, err := l.ensure(ctx)
	if err != nil {
		return err
	}
	return exec.Close(ctx, req)
}

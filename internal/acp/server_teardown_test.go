package acp

import (
	"context"
	"sync"
	"testing"
	"time"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/jobs"
)

type teardownFactory struct {
	dir            string
	grace          time.Duration
	mu             sync.Mutex
	manager        *jobs.Manager
	timeoutDetails []string
}

func (f *teardownFactory) SessionDir() string { return f.dir }

func (f *teardownFactory) NewSession(_ context.Context, p SessionParams) (*control.Controller, error) {
	sink := event.FuncSink(func(e event.Event) {
		if e.Text != "Background job teardown timed out." {
			return
		}
		f.mu.Lock()
		f.timeoutDetails = append(f.timeoutDetails, e.Detail)
		f.mu.Unlock()
	})
	jm := jobs.NewManager(sink, jobs.WithTeardownGrace(f.grace))
	f.mu.Lock()
	f.manager = jm
	f.mu.Unlock()
	runner := &fakeRunner{
		sink:     p.Sink,
		behavior: func(context.Context, event.Sink, string) error { return nil },
	}
	return control.New(control.Options{
		Runner:     runner,
		Sink:       p.Sink,
		SessionDir: f.dir,
		Jobs:       jm,
	}), nil
}

func (f *teardownFactory) lastManager(t *testing.T) *jobs.Manager {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.manager == nil {
		t.Fatal("session manager was not created")
	}
	return f.manager
}

func (f *teardownFactory) teardownTimeoutDetails() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.timeoutDetails...)
}

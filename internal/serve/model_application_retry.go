package serve

import (
	"context"
	"errors"
	"log/slog"
	"reasonix/internal/control"
	"reasonix/internal/secrets"
	"sync"
	"time"
)

// One event-driven retry owner per foreground binding. A failed construction
// is not requeued; the next explicit retry or saved revision initiates it.
type modelApplicationRetry struct {
	mu                      sync.Mutex
	owner                   control.SessionAPI
	pending, running, dirty bool
	failedRevision          string
	failure                 string
}

func (s *Server) cachedModelApplicationFailureLocked(ctx context.Context) error {
	r := &s.modelApplicationRetry
	owner := s.ctl()
	r.mu.Lock()
	revision, message, previous := r.failedRevision, r.failure, r.owner
	r.mu.Unlock()
	if previous != owner || revision == "" {
		return nil
	}
	d := s.modelApplicationDetailsLocked(ctx)
	if d != nil && d.DesiredRevision == revision {
		return errors.New(message)
	}
	return nil
}

func (s *Server) recordModelApplicationFailureLocked(ctx context.Context, err error) {
	var failure *modelConstructionError
	if !errors.As(err, &failure) {
		return
	}
	owner := s.ctl()
	r := &s.modelApplicationRetry
	r.mu.Lock()
	r.owner, r.failedRevision, r.failure = owner, failure.revision, secrets.RedactCredentials(err.Error())
	r.mu.Unlock()
	slog.Warn("model configuration application failed", "phase", "build")
}

type modelConstructionError struct {
	cause    error
	revision string
}

func (e *modelConstructionError) Error() string { return e.cause.Error() }
func (e *modelConstructionError) Unwrap() error { return e.cause }

func (s *Server) modelConstructionFailure(err error) error {
	revision := ""
	if settings := s.managedModels; settings != nil && settings.SourceToken != "" {
		revision = settings.Revision
	} else if c, ok := s.ctl().(*control.Controller); ok {
		_, revision, _ = c.ModelSettingsState()
	}
	return &modelConstructionError{cause: err, revision: revision}
}

func (s *Server) deferModelApplicationLocked() {
	r := &s.modelApplicationRetry
	owner := s.ctl()
	r.mu.Lock()
	r.owner = owner
	r.pending = true
	r.failedRevision = ""
	r.failure = ""
	r.mu.Unlock()
	s.kickModelApplication()
}

func (s *Server) kickModelApplication() {
	r := &s.modelApplicationRetry
	r.mu.Lock()
	if !r.pending {
		r.mu.Unlock()
		return
	}
	r.dirty = true
	if r.running {
		r.mu.Unlock()
		return
	}
	r.running = true
	r.mu.Unlock()
	go func() {
		for {
			s.bindMu.Lock()
			r.mu.Lock()
			owner := r.owner
			r.dirty = false
			r.mu.Unlock()
			if s.ctl() != owner || !control.ModelReplacementBlocked(owner) {
				r.mu.Lock()
				r.pending = false
				r.mu.Unlock()
				if s.ctl() == owner {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					_ = s.refreshRunModelSettingsLocked(ctx)
					cancel()
				}
			}
			s.bindMu.Unlock()
			r.mu.Lock()
			again := r.pending && r.dirty
			if !again {
				r.running = false
			}
			r.mu.Unlock()
			if !again {
				return
			}
		}
	}()
}

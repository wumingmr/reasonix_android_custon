package main

import (
	"log/slog"
	"time"
)

func (c *desktopShutdownCoordinator) runStep(name string, run func()) {
	_ = c.runErrorStep(name, func() error { run(); return nil })
}

// The coordinator serializes attempts. Only successful steps are checkpointed;
// a retry must revisit a failed service while preserving completed resources.
func (c *desktopShutdownCoordinator) runErrorStep(name string, run func() error) (err error) {
	c.mu.Lock()
	done := c.finished[name]
	c.mu.Unlock()
	if done {
		return nil
	}
	started := time.Now()
	slog.Debug("desktop: shutdown step", "step", name, "outcome", "started")
	completed := false
	defer func() {
		outcome := "failed"
		if completed {
			outcome = "completed"
		}
		if completed {
			slog.Debug("desktop: shutdown step", "step", name, "outcome", outcome, "duration_ms", time.Since(started).Milliseconds())
		} else {
			slog.Warn("desktop: shutdown step failed", "step", name, "outcome", outcome, "duration_ms", time.Since(started).Milliseconds(), "err", err)
		}
	}()
	if err = run(); err != nil {
		return err
	}
	c.mu.Lock()
	c.finished[name] = true
	c.mu.Unlock()
	completed = true
	return nil
}

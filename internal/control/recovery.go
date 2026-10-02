package control

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"reasonix/internal/agent"
	"reasonix/internal/recovery"
)

// ResolveRecovery is retained as a wire-compatible endpoint for older clients.
// Auto Guard is retired: historical cards are read-only and cannot authorize or
// replay an operation.
func (c *Controller) ResolveRecovery(id string, action agent.RecoveryAction, feedback string) error {
	c.promptResolveMu.Lock()
	defer c.promptResolveMu.Unlock()
	return c.resolveRecoveryLocked(id, action, feedback)
}

func (c *Controller) resolveRecoveryLocked(id string, action agent.RecoveryAction, feedback string) error {
	return fmt.Errorf("recovery_retired: Auto Guard actions are read-only historical records and cannot confirm or replay operations")
}

func clipUTF8(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// These lifecycle methods remain as no-op source-compatibility hooks while old
// session containers and sidecars remain readable. They never construct a gate,
// restore an Episode, emit a prompt, or persist retired runtime state.
func (c *Controller) loadRecoveryState(string)          {}
func (c *Controller) resetRecoveryForNewSession(string) {}
func (c *Controller) carryRecoveryState(string)         {}
func (c *Controller) CarryRecoveryFrom(*Controller)     {}
func (c *Controller) flushRecoveryPersistence(string)   {}
func (c *Controller) saveRecoveryState(string)          {}
func (c *Controller) ReplayUnresolvedRecoveries()       {}

// RecoveryMetrics remains for consumers compiled against the old API. With no
// Auto Guard runtime it always reports zero.
func (c *Controller) RecoveryMetrics() recovery.Metrics { return recovery.Metrics{} }
func (c *Controller) DrainRecoveryMetrics() recovery.Metrics {
	return recovery.Metrics{}
}

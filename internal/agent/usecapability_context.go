package agent

import (
	"context"

	"reasonix/internal/capability"
	"reasonix/internal/tool"
)

// contextualEntry overlays live availability on a copy. The cached catalog
// and its fingerprint stay stable; discovery cannot authorize a later call,
// which must recheck the same tool contract at execution time.
func (t *UseCapabilityTool) contextualEntry(ctx context.Context, entry capability.Entry) capability.Entry {
	if entry.Kind != capability.KindTool || entry.Status != capability.StatusReady || t.registry == nil {
		return entry
	}
	if target, ok := t.registry.Get(entry.ToolName); ok {
		if reason := tool.ContextualUnavailableReason(ctx, target); reason != "" {
			entry.Status = capability.StatusDisabled
			entry.FailureReason = reason
		}
	}
	return entry
}

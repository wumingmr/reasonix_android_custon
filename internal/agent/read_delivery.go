package agent

import (
	"context"
	"crypto/sha256"

	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// readDelivery contains no source text. References always name an original
// delivery, never another reference. Both maps are owned by the run loop.
type readDelivery struct {
	digest [32]byte
}

// freezeVisibleReads is called for the exact request used by a sampling
// attempt, after projection, interception and admission. Missing or rewritten
// original text makes a reference ineligible, even if RawContent is retained.
func (a *Agent) freezeVisibleReads(messages []provider.Message) {
	visible := make(map[string]readDelivery)
	for _, msg := range messages {
		if msg.Role != provider.RoleTool || msg.LocalOnly {
			continue
		}
		if d, ok := a.reads.deliveries[msg.ToolCallID]; ok && sha256.Sum256([]byte(msg.Content)) == d.digest {
			visible[msg.ToolCallID] = d
		}
	}
	a.reads.visible = visible
}

func (a *Agent) finalizedReadEnvelope(ctx context.Context, call provider.ToolCall, o toolOutcome) (tool.ReadResultEnvelope, bool) {
	return a.readResultEnvelopeFor(ctx, call, o)
}

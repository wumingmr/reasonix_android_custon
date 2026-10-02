package session

import "reasonix/internal/provider"

// rejectedToolResult contains only host-recorded identities and state, never
// tool bodies or model-authored claims about whether a call executed.
type rejectedToolResult struct {
	ToolCallID string                `json:"toolCallId"`
	Name       string                `json:"name"`
	State      provider.ToolRunState `json:"state"`
}

func noteRejectedToolResult(p *Projection, m provider.Message) {
	if m.ID == "" {
		return
	}
	// Upserts replace authority as well as content: a later executed/unknown
	// result must not inherit an earlier refusal for the same message identity.
	delete(p.RejectedToolResults, m.ID)
	if m.LocalOnly || m.Role != provider.RoleTool || m.ToolCallID == "" || m.Name == "" {
		return
	}
	if m.ToolRunState != provider.ToolRunNotStarted && m.ToolRunState != provider.ToolRunCancelled {
		return
	}
	if p.RejectedToolResults == nil {
		p.RejectedToolResults = make(map[string]rejectedToolResult)
	}
	p.RejectedToolResults[m.ID] = rejectedToolResult{ToolCallID: m.ToolCallID, Name: m.Name, State: m.ToolRunState}
}

// restoreRejectedToolResults operates on a detached execution snapshot. The
// executor needs this proof for RepairRejectedArguments before ModelMessages
// removes local metadata at the outbound provider boundary. Canonical arguments
// and the stored wire projection stay unchanged.
func restoreRejectedToolResults(p *Projection) {
	if len(p.RejectedToolResults) == 0 {
		return
	}
	for i := range p.ModelMessages {
		m := &p.ModelMessages[i]
		if m.LocalOnly || m.Role != provider.RoleTool || m.ToolRunState != "" {
			continue
		}
		evidence, ok := p.RejectedToolResults[m.ID]
		if !ok || evidence.ToolCallID != m.ToolCallID || evidence.Name != m.Name {
			continue
		}
		if evidence.State == provider.ToolRunNotStarted || evidence.State == provider.ToolRunCancelled {
			m.ToolRunState = evidence.State
		}
	}
}

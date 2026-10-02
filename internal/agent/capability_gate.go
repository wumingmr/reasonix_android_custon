package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"reasonix/internal/capability"
	"reasonix/internal/skill"
)

// capabilityGateState is one user turn's gate memory, scoped to the same turn
// as the ledger it reads: whether the prefer reminder has already been spent,
// and which kind of miss was reported, so a later clean gate is audited as a
// recovery instead of a first pass. Zeroing the struct is the turn reset.
type capabilityGateState struct {
}

// SeedCapabilityRoute installs the turn's route decision into the capability ledger.
func (a *Agent) SeedCapabilityRoute(decision capability.RouteDecision) {
	if a == nil {
		return
	}
	if a.capabilityLedger == nil {
		a.capabilityLedger = capability.NewLedger()
	}
	a.capabilityLedger.Reset()
	a.capabilityLedger.SeedCandidates(decision)
	a.capabilityGate = capabilityGateState{}
}

// CapabilityLedger returns the turn-scoped capability ledger (may be nil).
func (a *Agent) CapabilityLedger() *capability.Ledger {
	if a == nil {
		return nil
	}
	return a.capabilityLedger
}

// CapabilityAudit returns the non-persisted capability metrics sink (may be nil).
func (a *Agent) CapabilityAudit() *capability.Audit {
	if a == nil {
		return nil
	}
	return a.capabilityAudit
}

func (a *Agent) noteCapabilityInvocation(toolName string, args json.RawMessage, callErr error) {
	if a == nil || a.capabilityLedger == nil {
		return
	}
	// Successful/failed proxied MCP calls execute the resolved target
	// directly, so this is the single audit point for action=call (inspect,
	// decline, and resolve-time unavailability are counted in ResolveCall,
	// which returns before this runs).
	if toolName == "use_capability" && a.capabilityAudit != nil {
		var p struct {
			Action string `json:"action"`
		}
		_ = json.Unmarshal(args, &p)
		if strings.EqualFold(strings.TrimSpace(p.Action), "call") {
			a.capabilityAudit.RecordMCPProxy(false, true, callErr != nil)
		}
	}
	id := capabilityIDFromToolCall(toolName, args)
	if id == "" {
		return
	}
	if callErr != nil {
		a.capabilityLedger.MarkFailed(id, callErr.Error())
		if a.capabilityAudit != nil && strings.HasPrefix(id, "skill:") {
			a.capabilityAudit.RecordSkill(true, errors.Is(callErr, skill.ErrInvocationUnavailable))
		}
		return
	}
	a.capabilityLedger.MarkSucceeded(id)
	if a.capabilityAudit != nil && strings.HasPrefix(id, "skill:") {
		a.capabilityAudit.RecordSkill(false, false)
	}
}

func capabilityIDFromToolCall(toolName string, args json.RawMessage) string {
	switch toolName {
	case "run_skill", "read_skill", "read_only_skill", "explore", "research", "review", "security_review":
		var p struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(args, &p)
		name := strings.TrimSpace(p.Name)
		if name == "" {
			// Dedicated wrappers use the tool name as the skill name.
			switch toolName {
			case "explore", "research", "review", "security_review":
				name = toolName
			}
		}
		if name == "security_review" {
			name = "security-review"
		}
		if name == "" {
			return ""
		}
		return "skill:" + name
	case "use_capability":
		var p struct {
			CapabilityID string `json:"capability_id"`
		}
		_ = json.Unmarshal(args, &p)
		return strings.TrimSpace(p.CapabilityID)
	default:
		if server, raw, ok := splitMCP(toolName); ok {
			return "mcp-tool:" + server + "/" + raw
		}
	}
	return ""
}

func splitMCP(name string) (server, raw string, ok bool) {
	const prefix = "mcp__"
	if !strings.HasPrefix(name, prefix) {
		return "", "", false
	}
	rest := name[len(prefix):]
	parts := strings.SplitN(rest, "__", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// ReviewWarnings returns warn-level review findings collected this turn.
func (a *Agent) ReviewWarnings() []string {
	if a == nil {
		return nil
	}
	return append([]string(nil), a.turn.reviewWarnings...)
}

// FormatReviewWarningsForSummary builds a short appendix for the final answer.
func FormatReviewWarningsForSummary(warnings []string) string {
	if len(warnings) == 0 {
		return ""
	}
	return "Review warnings:\n- " + strings.Join(warnings, "\n- ")
}

// ensure string used
var _ = fmt.Sprintf

package main

import (
	"encoding/json"
	"strings"
)

func remotePromptCacheKind(kind string) string {
	switch kind {
	case "ask":
		return "ask_request"
	case "mcp":
		return "mcp_interaction"
	case "approval", "plan", "recovery":
		return "approval_request"
	default:
		return ""
	}
}

func pendingFrameMatchesTarget(frame json.RawMessage, target RemotePromptTarget) bool {
	var probe struct {
		RuntimeEpoch string `json:"runtimeEpoch"`
		Approval     *struct {
			ID, TurnID string
		} `json:"approval"`
		Ask *struct {
			ID, TurnID string
		} `json:"ask"`
		MCPInteraction *struct {
			ID, TurnID string
		} `json:"mcpInteraction"`
	}
	if json.Unmarshal(frame, &probe) != nil {
		return false
	}
	id, turnID := "", ""
	switch target.Kind {
	case "ask":
		if probe.Ask != nil {
			id, turnID = probe.Ask.ID, probe.Ask.TurnID
		}
	case "mcp":
		if probe.MCPInteraction != nil {
			id, turnID = probe.MCPInteraction.ID, probe.MCPInteraction.TurnID
		}
	default:
		if probe.Approval != nil {
			id, turnID = probe.Approval.ID, probe.Approval.TurnID
		}
	}
	return id == target.PromptID && turnID == target.TurnID && probe.RuntimeEpoch == target.RuntimeEpoch
}

func (a *App) clearRemotePendingPromptExact(target RemotePromptTarget) {
	cacheKind := remotePromptCacheKind(strings.TrimSpace(target.Kind))
	if cacheKind == "" {
		return
	}
	a.remoteTabMu.Lock()
	var meta TabMeta
	changed := false
	if tab := a.remoteTabs[target.TabID]; tab != nil && tab.ref.HostID == target.HostID &&
		tab.session.sessionID == target.SessionID && tab.gen == target.SessionGeneration {
		key := cacheKind + ":" + strings.TrimSpace(target.PromptID)
		if frame := tab.pendingEvents[key]; pendingFrameMatchesTarget(frame, target) {
			delete(tab.pendingEvents, key)
			tab.runtime.revision++
			pending := len(tab.pendingEvents) > 0
			changed = tab.runtime.pendingPrompt != pending
			tab.runtime.pendingPrompt = pending
			meta = remoteTabMetaLocked(tab)
		}
	}
	a.remoteTabMu.Unlock()
	if changed {
		a.emitRemoteEvent("remote-tab:updated", meta)
	}
}

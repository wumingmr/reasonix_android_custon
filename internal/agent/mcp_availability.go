package agent

import (
	"errors"
	"fmt"
	"strings"

	"reasonix/internal/config"
	"reasonix/internal/plugin"
)

func (r *MCPCapabilityRuntime) serverRegistered(server string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	_, ok := r.servers[strings.TrimSpace(server)]
	r.mu.RUnlock()
	return ok
}

func mcpServerUnregisteredMessage(server string) string {
	return fmt.Sprintf("MCP server %q is not registered in this session", server)
}

func mcpServerDisabledMessage(server string) string {
	return fmt.Sprintf("MCP server %q is disabled in this session", server)
}

// mcpServerOffMessage carries the host's reason a server is off. One the
// project declared waits for the user, and only the user can change that.
func mcpServerOffMessage(server string, decision config.MCPDecision) string {
	if !decision.AwaitsUser() {
		return mcpServerDisabledMessage(server)
	}
	return fmt.Sprintf("MCP server %q is off [%s]: this project declares it, and it starts only after the user reviews and enables it (reasonix mcp enable %s, /mcp connect, or the MCP panel). Tell the user if it is needed.", server, decision.Code(), server)
}

// serverDecision resolves why entry is off; an enabled server is simply on.
func serverDecision(entry config.PluginEntry, spec plugin.Spec, enabled bool) config.MCPDecision {
	if enabled {
		return config.MCPDecisionOn
	}
	if d := config.MCPServerDecision(entry, spec.WorkspaceRoot); d.AwaitsUser() {
		return d
	}
	return config.MCPDecisionOff
}

func mcpServerUnregisteredError(server string) error {
	return errors.New(mcpServerUnregisteredMessage(server))
}

func mcpServerOffError(server string, decision config.MCPDecision) error {
	return errors.New(mcpServerOffMessage(server, decision))
}

func (t *UseCapabilityTool) serverRegistered(server string) bool {
	if t.runtime != nil {
		return t.runtime.serverRegistered(server)
	}
	// Standalone proxies have no authoritative runtime inventory to miss from.
	return true
}

// serverUnavailableReason distinguishes a disabled server from one this
// session never registered so the refusal points to the correct remedy.
func (t *UseCapabilityTool) serverUnavailableReason(server string) string {
	if !t.serverRegistered(server) {
		return mcpServerUnregisteredMessage(server)
	}
	if t.runtime != nil {
		t.runtime.mu.RLock()
		configured := t.runtime.servers[strings.TrimSpace(server)]
		t.runtime.mu.RUnlock()
		return mcpServerOffMessage(server, configured.decision)
	}
	return mcpServerDisabledMessage(server)
}
